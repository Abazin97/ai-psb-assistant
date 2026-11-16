package models

import (
	"time"

	"github.com/google/uuid"
)

type Chat struct {
	ID           uuid.UUID `json:"id"`
	OwnerID      string    `json:"-"`
	Title        string    `json:"title"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
