package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"backend/internal/domain/models"
	"backend/internal/repository"
	"backend/internal/service"

	"github.com/google/uuid"
)

type App interface {
	CreateChat(context.Context, string) (models.Chat, error)
	ListChats(context.Context, string, int) ([]models.Chat, error)
	GetChat(context.Context, string, uuid.UUID) (models.Chat, []models.Message, error)
	DeleteChat(context.Context, string, uuid.UUID) error
	Ask(context.Context, string, uuid.UUID, string) (repository.Exchange, error)
	Document(context.Context, string) (service.DocumentContent, error)
	Health(context.Context) service.Health
}

type Handler struct {
	app          App
	maxBodyBytes int64
	cookieSecure bool
	logger       *slog.Logger
}

var documentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func New(app App, maxBodyBytes int64, cookieSecure bool, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{app: app, maxBodyBytes: maxBodyBytes, cookieSecure: cookieSecure, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chats", h.createChat)
	mux.HandleFunc("GET /api/chats", h.listChats)
	mux.HandleFunc("GET /api/chats/{id}", h.getChat)
	mux.HandleFunc("DELETE /api/chats/{id}", h.deleteChat)
	mux.HandleFunc("POST /api/chats/{id}/messages", h.ask)
	mux.HandleFunc("GET /api/documents/{doc_id}", h.getDocument)
	mux.HandleFunc("GET /api/health", h.health)
	var handler = h.clientID(mux)
	handler = recoverPanic(h.logger, handler)
	handler = accessLog(h.logger, handler)
	handler = withRequestID(handler)
	return handler
}

func (h *Handler) clientID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("client_id")
		ownerID := ""
		if err == nil && validClientID(cookie.Value) {
			ownerID = cookie.Value
		} else {
			id, genErr := newClientID()
			if genErr != nil {
				h.writeError(w, http.StatusInternalServerError, "internal", "Внутренняя ошибка")
				h.logger.Error("generate client id", "error", genErr)
				return
			}
			ownerID = id
			http.SetCookie(w, &http.Cookie{
				Name: "client_id", Value: ownerID, Path: "/", HttpOnly: true,
				Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode,
			})
		}
		ctx := context.WithValue(r.Context(), ownerContextKey{}, ownerID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type ownerContextKey struct{}

func (h *Handler) ownerID(r *http.Request) string {
	id, _ := r.Context().Value(ownerContextKey{}).(string)
	return id
}

func validClientID(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) >= 16
}

func newClientID() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func (h *Handler) createChat(w http.ResponseWriter, r *http.Request) {
	chat, err := h.app.CreateChat(r.Context(), h.ownerID(r))
	if err != nil {
		h.writeAppError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, struct {
		Chat models.Chat `json:"chat"`
	}{chat})
}

func (h *Handler) listChats(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if values, ok := r.URL.Query()["limit"]; ok {
		if len(values) != 1 || values[0] == "" {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "Некорректный параметр limit")
			return
		}
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > 200 {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "Параметр limit должен быть от 1 до 200")
			return
		}
		limit = parsed
	}
	chats, err := h.app.ListChats(r.Context(), h.ownerID(r), limit)
	if err != nil {
		h.writeAppError(w, r, err)
		return
	}
	if chats == nil {
		chats = []models.Chat{}
	}
	h.writeJSON(w, http.StatusOK, struct {
		Chats []models.Chat `json:"chats"`
	}{chats})
}

func (h *Handler) getChat(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "Некорректный идентификатор чата")
		return
	}
	chat, messages, err := h.app.GetChat(r.Context(), h.ownerID(r), id)
	if err != nil {
		h.writeAppError(w, r, err)
		return
	}
	if messages == nil {
		messages = []models.Message{}
	}
	h.writeJSON(w, http.StatusOK, struct {
		Chat     models.Chat      `json:"chat"`
		Messages []models.Message `json:"messages"`
	}{chat, messages})
}

func (h *Handler) deleteChat(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "Некорректный идентификатор чата")
		return
	}
	if err := h.app.DeleteChat(r.Context(), h.ownerID(r), id); err != nil {
		h.writeAppError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ask(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Ожидается Content-Type: application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	var input struct {
		Text string `json:"text"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Тело запроса слишком большое")
			return
		}
		h.writeError(w, http.StatusBadRequest, "invalid_request", "Некорректное тело запроса")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Тело запроса слишком большое")
			return
		}
		h.writeError(w, http.StatusBadRequest, "invalid_request", "В запросе должно быть одно JSON-значение")
		return
	}
	if strings.TrimSpace(input.Text) == "" {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "Текст вопроса не должен быть пустым")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "Некорректный идентификатор чата")
		return
	}
	exchange, err := h.app.Ask(r.Context(), h.ownerID(r), id, input.Text)
	if err != nil {
		if errors.Is(err, service.ErrInvalidText) {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "Текст вопроса пустой или слишком длинный")
			return
		}
		h.writeAppError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, struct {
		UserMessage      models.Message `json:"user_message"`
		AssistantMessage models.Message `json:"assistant_message"`
	}{exchange.User, exchange.Assistant})
}

func (h *Handler) getDocument(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("doc_id")
	if !documentIDPattern.MatchString(docID) {
		h.writeError(w, http.StatusNotFound, "document_not_found", "Документ не найден")
		return
	}
	document, err := h.app.Document(r.Context(), docID)
	if err != nil {
		h.writeAppError(w, r, err)
		return
	}
	defer document.Body.Close()
	contentType := document.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if document.ContentDisposition != "" {
		w.Header().Set("Content-Disposition", document.ContentDisposition)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	if _, err := io.Copy(w, document.Body); err != nil && r.Context().Err() == nil {
		h.logger.Error("stream document", "error", err)
	}
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	health := h.app.Health(r.Context())
	if r.Context().Err() != nil {
		return
	}
	status := http.StatusOK
	if health.Status != "ok" {
		status = http.StatusServiceUnavailable
	}
	h.writeJSON(w, status, health)
}

func (h *Handler) writeAppError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	status, code, message := http.StatusInternalServerError, "internal", "Внутренняя ошибка"
	switch {
	case errors.Is(err, repository.ErrNotFound):
		status, code, message = http.StatusNotFound, "chat_not_found", "Чат не найден"
	case errors.Is(err, service.ErrChatBusy):
		status, code, message = http.StatusConflict, "chat_busy", "В этом чате уже выполняется запрос"
	case errors.Is(err, service.ErrMLTimeout):
		status, code, message = http.StatusGatewayTimeout, "ml_timeout", "Истекло время ожидания ответа"
	case errors.Is(err, service.ErrMLUnavailable):
		status, code, message = http.StatusServiceUnavailable, "ml_unavailable", "Сервис ответов временно недоступен"
	case errors.Is(err, service.ErrDocumentNotFound):
		status, code, message = http.StatusNotFound, "document_not_found", "Документ не найден"
	case errors.Is(err, service.ErrMLResponse), errors.Is(err, repository.ErrMetaTooLarge):
		status, code, message = http.StatusBadGateway, "ml_error", "Не удалось обработать ответ сервиса"
	}
	if status == http.StatusInternalServerError || status == http.StatusBadGateway {
		h.logger.Error("request failed", "error", err)
	}
	h.writeError(w, status, code, message)
}

func (h *Handler) writeError(w http.ResponseWriter, status int, code, message string) {
	h.writeJSON(w, status, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		h.logger.Error("write JSON response", "error", err)
	}
}
