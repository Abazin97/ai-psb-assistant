package repository

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"backend/internal/domain"
	"backend/internal/domain/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_init.sql
var schemaSQL string

type Repo struct{ pool *pgxpool.Pool }

var _ Repository = (*Repo)(nil)

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) Ping(ctx context.Context) error { return r.pool.Ping(ctx) }

func (r *Repo) Migrate(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, schemaSQL)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

const chatCols = `id, owner_id, title, message_count, created_at, updated_at`

func scanChat(row pgx.Row) (models.Chat, error) {
	var c models.Chat
	err := row.Scan(&c.ID, &c.OwnerID, &c.Title, &c.MessageCount, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Chat{}, ErrNotFound
	}
	return c, err
}

func (r *Repo) CreateChat(ctx context.Context, ownerID string) (models.Chat, error) {
	return scanChat(r.pool.QueryRow(ctx,
		`INSERT INTO chats (owner_id) VALUES ($1) RETURNING `+chatCols, ownerID))
}

func (r *Repo) ListChats(ctx context.Context, ownerID string, limit int) ([]models.Chat, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+chatCols+` FROM chats WHERE owner_id = $1 ORDER BY updated_at DESC LIMIT $2`,
		ownerID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.Chat, error) {
		var c models.Chat
		err := row.Scan(&c.ID, &c.OwnerID, &c.Title, &c.MessageCount, &c.CreatedAt, &c.UpdatedAt)
		return c, err
	})
}

func (r *Repo) GetChat(ctx context.Context, ownerID string, chatID uuid.UUID) (models.Chat, error) {
	return scanChat(r.pool.QueryRow(ctx,
		`SELECT `+chatCols+` FROM chats WHERE id = $1 AND owner_id = $2`, chatID, ownerID))
}

func (r *Repo) DeleteChat(ctx context.Context, ownerID string, chatID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chats WHERE id = $1 AND owner_id = $2`, chatID, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) DeleteStaleChats(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM chats WHERE updated_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const msgCols = `id, chat_id, seq, role, kind, content, meta, latency_ms, created_at`

func scanMessage(row pgx.CollectableRow) (models.Message, error) {
	var m models.Message
	var meta []byte
	err := row.Scan(&m.ID, &m.ChatID, &m.Seq, &m.Role, &m.Kind, &m.Content, &meta, &m.LatencyMs, &m.CreatedAt)
	m.Meta = json.RawMessage(meta)
	return m, err
}

func (r *Repo) GetChatWithMessages(ctx context.Context, ownerID string, chatID uuid.UUID) (models.Chat, []models.Message, error) {
	chat, err := r.GetChat(ctx, ownerID, chatID)
	if err != nil {
		return models.Chat{}, nil, err
	}

	rows, err := r.pool.Query(ctx,
		`SELECT `+msgCols+` FROM messages WHERE chat_id = $1 ORDER BY seq`, chatID)
	if err != nil {
		return models.Chat{}, nil, err
	}
	msgs, err := pgx.CollectRows(rows, scanMessage)
	if err != nil {
		return models.Chat{}, nil, err
	}
	if len(msgs) == 0 {
		return chat, msgs, nil
	}

	index := make(map[uuid.UUID]int, len(msgs))
	for i, m := range msgs {
		index[m.ID] = i
	}

	srcRows, err := r.pool.Query(ctx, `
		SELECT s.message_id, s.ordinal, s.doc_id, s.doc_title, s.fragment_id, s.text,
		       s.start_offset, s.end_offset
		FROM message_sources s
		JOIN messages m ON m.id = s.message_id
		WHERE m.chat_id = $1
		ORDER BY m.seq, s.ordinal`, chatID)
	if err != nil {
		return models.Chat{}, nil, err
	}
	defer srcRows.Close()

	for srcRows.Next() {
		var mid uuid.UUID
		var s models.Source
		if err := srcRows.Scan(&mid, &s.Ordinal, &s.DocID, &s.DocTitle, &s.FragmentID, &s.Text,
			&s.StartOffset, &s.EndOffset); err != nil {
			return models.Chat{}, nil, err
		}
		if i, ok := index[mid]; ok {
			msgs[i].Sources = append(msgs[i].Sources, s)
		}
	}
	if err := srcRows.Err(); err != nil {
		return models.Chat{}, nil, err
	}
	return chat, msgs, nil
}

func (r *Repo) LastMessages(ctx context.Context, chatID uuid.UUID, n int) ([]models.Message, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+msgCols+` FROM (
			SELECT `+msgCols+` FROM messages WHERE chat_id = $1 ORDER BY seq DESC LIMIT $2
		) t ORDER BY seq`, chatID, n)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanMessage)
}

// SaveExchange: единственное место, где создаются сообщения.
// Один UPDATE резервирует два номера seq и держит блокировку строки чата до COMMIT,
// поэтому параллельные записи в один чат выстраиваются в очередь, а UNIQUE (chat_id, seq) страхует.
func (r *Repo) SaveExchange(ctx context.Context, in SaveExchangeInput) (Exchange, error) {
	var ex Exchange
	if len(in.Meta) > domain.MaxMetaBytes {
		return ex, ErrMetaTooLarge
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ex, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) // после Commit вернёт ErrTxClosed, это нормально

	userText := clean(in.UserText)

	// 1. Резервируем номера. В SET правая часть видит старые значения, поэтому
	//    CASE WHEN message_count = 0 срабатывает только на первом обмене чата.
	var last int
	err = tx.QueryRow(ctx, `
		UPDATE chats
		SET message_count = message_count + 2,
		    updated_at    = now(),
		    title         = CASE WHEN message_count = 0 THEN $3 ELSE title END
		WHERE id = $1 AND owner_id = $2
		RETURNING message_count`,
		in.ChatID, in.OwnerID, makeTitle(userText)).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		return ex, ErrNotFound
	}
	if err != nil {
		return ex, fmt.Errorf("reserve seq: %w", err)
	}

	// 2. Вопрос пользователя: seq = last-1
	ex.User = models.Message{
		ChatID: in.ChatID, Seq: last - 1, Role: "user", Kind: domain.KindText,
		Content: userText, Meta: json.RawMessage("{}"),
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO messages (chat_id, seq, role, kind, content)
		VALUES ($1, $2, 'user', 'text', $3)
		RETURNING id, created_at`,
		in.ChatID, ex.User.Seq, userText).Scan(&ex.User.ID, &ex.User.CreatedAt)
	if err != nil {
		return Exchange{}, fmt.Errorf("insert user message: %w", err)
	}

	// 3. Ответ ассистента: seq = last
	meta := in.Meta
	if len(meta) == 0 {
		meta = json.RawMessage("{}")
	}
	latency := in.LatencyMs
	ex.Assistant = models.Message{
		ChatID: in.ChatID, Seq: last, Role: "assistant", Kind: in.Kind,
		Content: clean(in.Answer), Meta: meta, LatencyMs: &latency,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO messages (chat_id, seq, role, kind, content, meta, latency_ms)
		VALUES ($1, $2, 'assistant', $3, $4, $5, $6)
		RETURNING id, created_at`,
		in.ChatID, last, in.Kind, ex.Assistant.Content, []byte(meta), latency,
	).Scan(&ex.Assistant.ID, &ex.Assistant.CreatedAt)
	if err != nil {
		return Exchange{}, fmt.Errorf("insert assistant message: %w", err)
	}

	// 4. Источники (снимок фрагментов на момент ответа)
	if len(in.Sources) > 0 {
		batch := &pgx.Batch{}
		sources := make([]models.Source, len(in.Sources))
		for i, s := range in.Sources {
			s.DocID, s.DocTitle = clean(s.DocID), clean(s.DocTitle)
			s.FragmentID, s.Text = clean(s.FragmentID), clean(s.Text)
			sources[i] = s
			batch.Queue(`
				INSERT INTO message_sources
				    (message_id, ordinal, doc_id, doc_title, fragment_id, text, start_offset, end_offset)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				ex.Assistant.ID, s.Ordinal, s.DocID, s.DocTitle, s.FragmentID, s.Text,
				s.StartOffset, s.EndOffset)
		}
		br := tx.SendBatch(ctx, batch)
		for range sources {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return Exchange{}, fmt.Errorf("insert source: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return Exchange{}, fmt.Errorf("insert sources: %w", err)
		}
		ex.Assistant.Sources = sources
	}

	if err := tx.Commit(ctx); err != nil {
		return Exchange{}, fmt.Errorf("commit: %w", err)
	}
	return ex, nil
}

// clean убирает то, что Postgres не принимает в TEXT: NUL-байты и невалидный UTF-8.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "")
	return strings.ReplaceAll(s, "\x00", "")
}

// makeTitle: название чата из первого вопроса (до 60 символов, одной строкой).
func makeTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "Новый чат"
	}
	const maxRunes = 60
	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes]) + "…"
	}
	return s
}
