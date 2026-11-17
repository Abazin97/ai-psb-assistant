package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"backend/internal/domain"
	"backend/internal/domain/models"
	"backend/internal/repository"

	"github.com/google/uuid"
)

var (
	ErrInvalidText = errors.New("invalid question text")
	ErrChatBusy    = errors.New("chat is busy")
)

type Health struct {
	Status string `json:"status"`
	DB     string `json:"db"`
	ML     string `json:"ml"`
	MLMode string `json:"ml_mode"`
	KBHash string `json:"kb_hash"`
}

type Service struct {
	repo         repository.Repository
	ml           ML
	maxTextChars int
	historyLimit int
	mlTimeout    time.Duration
	locks        *chatLocks
}

func New(repo repository.Repository, ml ML, maxTextChars, historyLimit int, mlTimeout time.Duration) *Service {
	return &Service{
		repo: repo, ml: ml, maxTextChars: maxTextChars,
		historyLimit: historyLimit, mlTimeout: mlTimeout, locks: newChatLocks(),
	}
}

func (s *Service) CreateChat(ctx context.Context, ownerID string) (models.Chat, error) {
	return s.repo.CreateChat(ctx, ownerKey(ownerID))
}

func (s *Service) ListChats(ctx context.Context, ownerID string, limit int) ([]models.Chat, error) {
	return s.repo.ListChats(ctx, ownerKey(ownerID), limit)
}

func (s *Service) GetChat(ctx context.Context, ownerID string, chatID uuid.UUID) (models.Chat, []models.Message, error) {
	return s.repo.GetChatWithMessages(ctx, ownerKey(ownerID), chatID)
}

func (s *Service) DeleteChat(ctx context.Context, ownerID string, chatID uuid.UUID) error {
	return s.repo.DeleteChat(ctx, ownerKey(ownerID), chatID)
}

func (s *Service) Ask(ctx context.Context, ownerID string, chatID uuid.UUID, text string) (repository.Exchange, error) {
	var empty repository.Exchange
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > s.maxTextChars {
		return empty, ErrInvalidText
	}
	owner := ownerKey(ownerID)
	if _, err := s.repo.GetChat(ctx, owner, chatID); err != nil {
		return empty, err
	}
	release, ok := s.locks.tryLock(chatID)
	if !ok {
		return empty, ErrChatBusy
	}
	defer release()

	previous, err := s.repo.LastMessages(ctx, chatID, s.historyLimit)
	if err != nil {
		return empty, err
	}
	history := make([]HistoryMessage, 0, len(previous))
	for _, message := range previous {
		history = append(history, HistoryMessage{Role: message.Role, Content: message.Content, Kind: message.Kind})
	}

	callCtx, cancel := withTimeout(ctx, s.mlTimeout)
	started := time.Now()
	answer, err := s.ml.Answer(callCtx, AnswerRequest{Question: text, History: history})
	cancel()
	if err != nil {
		return empty, err
	}
	if len(answer.Meta) > domain.MaxMetaBytes {
		return empty, repository.ErrMetaTooLarge
	}
	if answer.Kind != domain.KindAnswer {
		answer.Sources = nil
	}
	return s.repo.SaveExchange(ctx, repository.SaveExchangeInput{
		OwnerID: owner, ChatID: chatID, UserText: text, Answer: answer.Answer,
		Kind: answer.Kind, Meta: answer.Meta, LatencyMs: int(time.Since(started).Milliseconds()),
		Sources: answer.Sources,
	})
}

func (s *Service) Document(ctx context.Context, docID string) (DocumentContent, error) {
	callCtx, cancel := withTimeout(ctx, s.mlTimeout)
	document, err := s.ml.Document(callCtx, docID)
	if err != nil {
		cancel()
		return DocumentContent{}, err
	}
	document.Body = &cancelReadCloser{ReadCloser: document.Body, cancel: cancel}
	return document, nil
}

func (s *Service) Health(ctx context.Context) Health {
	health := Health{Status: "ok", DB: "ok", ML: "ok", MLMode: "live"}
	dbCtx, cancelDB := context.WithTimeout(ctx, 2*time.Second)
	dbErr := s.repo.Ping(dbCtx)
	cancelDB()
	mlCtx, cancelML := context.WithTimeout(ctx, 2*time.Second)
	mlHealth, mlErr := s.ml.Health(mlCtx)
	cancelML()
	if dbErr != nil {
		health.DB = "error"
	}
	if mlErr != nil || mlHealth.Status != "ok" {
		health.ML = "error"
		health.MLMode = "unavailable"
	} else {
		health.KBHash = mlHealth.KBHash
		if mlHealth.Mode != "" {
			health.MLMode = mlHealth.Mode
		}
	}
	if health.DB != "ok" || health.ML != "ok" {
		health.Status = "degraded"
	}
	return health
}

func ownerKey(ownerID string) string {
	sum := sha256.Sum256([]byte(ownerID))
	return hex.EncodeToString(sum[:])
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

type chatLocks struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*chatLock
}

type chatLock struct {
	mu   sync.Mutex
	refs int
}

func newChatLocks() *chatLocks { return &chatLocks{byID: make(map[uuid.UUID]*chatLock)} }

func (l *chatLocks) tryLock(id uuid.UUID) (func(), bool) {
	l.mu.Lock()
	entry := l.byID[id]
	if entry == nil {
		entry = &chatLock{}
		l.byID[id] = entry
	}
	entry.refs++
	l.mu.Unlock()

	if !entry.mu.TryLock() {
		l.releaseRef(id, entry)
		return nil, false
	}
	return func() {
		entry.mu.Unlock()
		l.releaseRef(id, entry)
	}, true
}

func (l *chatLocks) releaseRef(id uuid.UUID, entry *chatLock) {
	l.mu.Lock()
	entry.refs--
	if entry.refs == 0 {
		delete(l.byID, id)
	}
	l.mu.Unlock()
}
