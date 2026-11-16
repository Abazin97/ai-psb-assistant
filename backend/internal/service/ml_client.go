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

	"backend/internal/domain/models"
	"backend/internal/requestmeta"
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
	Document(ctx context.Context, docID string) (io.ReadCloser, error)
	Health(ctx context.Context) (MLHealth, error)
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
	body, err := json.Marshal(request)
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
		Type    string `json:"type"`
		Answer  string `json:"answer"`
		Sources []struct {
			Ordinal    int    `json:"ordinal"`
			DocID      string `json:"doc_id"`
			DocTitle   string `json:"doc_title"`
			FragmentID string `json:"fragment_id"`
			Text       string `json:"text"`
			Start      *int   `json:"start"`
			End        *int   `json:"end"`
		} `json:"sources"`
		Meta json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return AnswerResponse{}, fmt.Errorf("%w: decode JSON: %v", ErrMLResponse, err)
	}
	if raw.Type != "answer" && raw.Type != "clarification" && raw.Type != "refusal" {
		return AnswerResponse{}, fmt.Errorf("%w: unknown answer type", ErrMLResponse)
	}
	if len(raw.Meta) == 0 {
		raw.Meta = json.RawMessage("{}")
	}
	var metaObject map[string]json.RawMessage
	if err := json.Unmarshal(raw.Meta, &metaObject); err != nil || metaObject == nil {
		return AnswerResponse{}, fmt.Errorf("%w: meta must be a JSON object", ErrMLResponse)
	}
	answer := AnswerResponse{
		Kind:    raw.Type,
		Answer:  raw.Answer,
		Meta:    append(json.RawMessage(nil), raw.Meta...),
		Sources: make([]models.Source, 0, len(raw.Sources)),
	}
	for _, source := range raw.Sources {
		answer.Sources = append(answer.Sources, models.Source{
			Ordinal: source.Ordinal, DocID: source.DocID, DocTitle: source.DocTitle,
			FragmentID: source.FragmentID, Text: source.Text,
			StartOffset: source.Start, EndOffset: source.End,
		})
	}
	return answer, nil
}

func (c *MLClient) Document(ctx context.Context, docID string) (io.ReadCloser, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/documents/"+url.PathEscape(docID), nil)
	if err != nil {
		return nil, fmt.Errorf("create ML document request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrDocumentNotFound
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: ML document endpoint returned HTTP %d", ErrMLResponse, resp.StatusCode)
	}
	return resp.Body, nil
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
