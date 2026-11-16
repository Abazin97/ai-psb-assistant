package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"backend/internal/requestmeta"
	"github.com/google/uuid"
)

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.NewRandom()
		if err != nil {
			writeMiddlewareError(w, http.StatusInternalServerError, "internal", "Внутренняя ошибка")
			return
		}
		requestID := id.String()
		w.Header().Set("X-Request-ID", requestID)
		ctx := requestmeta.WithRequestID(r.Context(), requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.Info("HTTP request",
				"request_id", requestmeta.RequestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration", time.Since(started),
			)
		}()
		next.ServeHTTP(sw, r)
	})
}

func recoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("HTTP handler panic",
					"request_id", requestmeta.RequestID(r.Context()),
					"panic", recovered,
				)
				if !sw.wroteHeader {
					writeMiddlewareError(sw, http.StatusInternalServerError, "internal", "Внутренняя ошибка")
				}
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func writeMiddlewareError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("{\"error\":{\"code\":\"" + code + "\",\"message\":\"" + message + "\"}}\n"))
}
