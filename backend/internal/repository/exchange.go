package repository

import (
	"encoding/json"

	"backend/internal/domain/models"

	"github.com/google/uuid"
)

type SaveExchangeInput struct {
	OwnerID   string
	ChatID    uuid.UUID
	UserText  string
	Answer    string
	Kind      string
	Meta      json.RawMessage
	LatencyMs int
	Sources   []models.Source
}

type Exchange struct {
	User      models.Message
	Assistant models.Message
}
