package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"backend/internal/domain"
	"backend/internal/domain/models"
	"backend/internal/requestmeta"

	"github.com/google/uuid"
)

var (
	ErrMLUnavailable    = errors.New("ML service unavailable")
	ErrMLTimeout        = errors.New("ML request timed out")
	ErrMLResponse       = errors.New("invalid ML response")
	ErrDocumentNotFound = errors.New("document not found")
)

type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Kind    string `json:"kind"`
}

type AnswerRequest struct {
	Question string           `json:"question"`
	History  []HistoryMessage `json:"history"`
}

type AnswerResponse struct {
	Kind    string
	Answer  string
	Meta    json.RawMessage
	Sources []models.Source
}

type MLHealth struct {
	Status string `json:"status"`
	KBHash string `json:"kb_hash"`
	Chunks int    `json:"chunks"`
	Mode   string `json:"mode,omitempty"`
}

type ML interface {
	Answer(ctx context.Context, request AnswerRequest) (AnswerResponse, error)
	Document(ctx context.Context, docID string) (DocumentContent, error)
	Health(ctx context.Context) (MLHealth, error)
}

type DocumentContent struct {
	Body               io.ReadCloser
	ContentType        string
	ContentDisposition string
}

type MLClient struct {
	baseURL string
	client  *http.Client
}

func NewMLClient(baseURL string) (*MLClient, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid ML_URL %q", baseURL)
	}
	return &MLClient{baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{}}, nil
}

func (c *MLClient) Answer(ctx context.Context, request AnswerRequest) (AnswerResponse, error) {
	// solution.py's batch input columns are id,question. Keep the same names
	// on the future HTTP boundary, and pack the chat history into question
	// because the ML script accepts a dialogue string rather than a history array.
	requestID := requestmeta.RequestID(ctx)
	if requestID == "" {
		requestID = uuid.NewString()
	}
	wireRequest := struct {
		ID       string `json:"id"`
		Question string `json:"question"`
	}{
		ID:       requestID,
		Question: formatMLDialogue(request),
	}
	body, err := json.Marshal(wireRequest)
	if err != nil {
		return AnswerResponse{}, fmt.Errorf("encode ML request: %w", err)
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/answer", bytes.NewReader(body))
	if err != nil {
		return AnswerResponse{}, fmt.Errorf("create ML request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return AnswerResponse{}, classifyTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AnswerResponse{}, fmt.Errorf("%w: ML returned HTTP %d", ErrMLResponse, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return AnswerResponse{}, fmt.Errorf("%w: read response: %v", ErrMLResponse, err)
	}
	var raw struct {
		Kind    string          `json:"kind"`
		ID      string          `json:"id"`
		Answer  string          `json:"answer"`
		Sources []models.Source `json:"sources"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return AnswerResponse{}, fmt.Errorf("%w: decode JSON: %v", ErrMLResponse, err)
	}
	if raw.ID != wireRequest.ID {
		return AnswerResponse{}, fmt.Errorf("%w: response id does not match request", ErrMLResponse)
	}
	if strings.TrimSpace(raw.Answer) == "" {
		return AnswerResponse{}, fmt.Errorf("%w: empty answer", ErrMLResponse)
	}
	kind := raw.Kind
	if kind == "" {
		// Accept older ML deployments during a rolling update.
		kind = domain.KindAnswer
	}
	if kind != domain.KindAnswer && kind != domain.KindClarification && kind != domain.KindRefusal {
		return AnswerResponse{}, fmt.Errorf("%w: unsupported answer kind %q", ErrMLResponse, kind)
	}
	sources := raw.Sources
	if kind != domain.KindAnswer {
		sources = nil
	}
	return AnswerResponse{
		Kind:    kind,
		Answer:  raw.Answer,
		Meta:    json.RawMessage(`{}`),
		Sources: sources,
	}, nil
}

func formatMLDialogue(request AnswerRequest) string {
	var dialogue strings.Builder
	for _, message := range request.History {
		var role string
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "user":
			role = "Пользователь"
		case "assistant":
			role = "Ассистент"
		default:
			continue
		}
		content := strings.Join(strings.Fields(message.Content), " ")
		if content == "" {
			continue
		}
		dialogue.WriteByte('[')
		dialogue.WriteString(role)
		dialogue.WriteString("]: ")
		dialogue.WriteString(content)
		dialogue.WriteByte('\n')
	}
	dialogue.WriteString("[Пользователь]: ")
	dialogue.WriteString(strings.Join(strings.Fields(request.Question), " "))
	return dialogue.String()
}

func (c *MLClient) Document(ctx context.Context, docID string) (DocumentContent, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/documents/"+url.PathEscape(docID), nil)
	if err != nil {
		return DocumentContent{}, fmt.Errorf("create ML document request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return DocumentContent{}, classifyTransportError(err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return DocumentContent{}, ErrDocumentNotFound
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return DocumentContent{}, fmt.Errorf("%w: ML document endpoint returned HTTP %d", ErrMLResponse, resp.StatusCode)
	}
	return DocumentContent{
		Body:               resp.Body,
		ContentType:        resp.Header.Get("Content-Type"),
		ContentDisposition: resp.Header.Get("Content-Disposition"),
	}, nil
}

func (c *MLClient) Health(ctx context.Context) (MLHealth, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return MLHealth{}, fmt.Errorf("create ML health request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return MLHealth{}, classifyTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return MLHealth{}, fmt.Errorf("%w: ML health returned HTTP %d", ErrMLResponse, resp.StatusCode)
	}
	var health MLHealth
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health); err != nil || health.Status != "ok" {
		return MLHealth{}, fmt.Errorf("%w: invalid ML health response", ErrMLResponse)
	}
	return health, nil
}

func classifyTransportError(err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return fmt.Errorf("%w: %v", ErrMLTimeout, err)
	}
	return fmt.Errorf("%w: %v", ErrMLUnavailable, err)
}

func (c *MLClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if id := requestmeta.RequestID(ctx); id != "" {
		req.Header.Set("X-Request-ID", id)
	}
	return req, nil
}

func withTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}
