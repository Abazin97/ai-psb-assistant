package repository

import (
	"context"
	"time"

	"backend/internal/domain/models"

	"github.com/google/uuid"
)

type Repository interface {
	Migrate(ctx context.Context) error
	Ping(ctx context.Context) error
	CreateChat(ctx context.Context, ownerID string) (models.Chat, error)
	ListChats(ctx context.Context, ownerID string, limit int) ([]models.Chat, error)
	GetChat(ctx context.Context, ownerID string, chatID uuid.UUID) (models.Chat, error)
	DeleteChat(ctx context.Context, ownerID string, chatID uuid.UUID) error
	DeleteStaleChats(ctx context.Context, olderThan time.Duration) (int64, error)
	GetChatWithMessages(ctx context.Context, ownerID string, chatID uuid.UUID) (models.Chat, []models.Message, error)
	LastMessages(ctx context.Context, chatID uuid.UUID, n int) ([]models.Message, error)
	SaveExchange(ctx context.Context, in SaveExchangeInput) (Exchange, error)
}
