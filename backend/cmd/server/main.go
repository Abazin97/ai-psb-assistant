package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backend/internal/config"
	httpapi "backend/internal/http"
	"backend/internal/repository"
	"backend/internal/service"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()

	repo := repository.New(pool)
	if err := repo.Migrate(ctx); err != nil {
		return err
	}
	var ml service.ML
	if cfg.MLMock {
		ml = service.NewMockML()
		logger.Warn("using mock ML service")
	} else {
		ml, err = service.NewMLClient(cfg.MLURL)
		if err != nil {
			return err
		}
	}
	app := service.New(repo, ml, cfg.MaxTextChars, cfg.HistoryLimit, cfg.MLTimeout)
	handler := httpapi.New(app, cfg.MaxBodyBytes, cfg.CookieSecure, logger)

	go cleanupStaleChats(ctx, repo, cfg.ChatTTL, logger)
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.MLTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "port", cfg.Port)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	}
}

func cleanupStaleChats(ctx context.Context, repo *repository.Repo, ttl time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := repo.DeleteStaleChats(ctx, ttl); err != nil && ctx.Err() == nil {
				logger.Error("delete stale chats", "error", err)
			}
		}
	}
}
