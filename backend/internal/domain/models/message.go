package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Message struct {
	ID        uuid.UUID       `json:"id"`
	ChatID    uuid.UUID       `json:"-"`
	Seq       int             `json:"seq"`
	Role      string          `json:"role"`
	Kind      string          `json:"kind"`
	Content   string          `json:"content"`
	Meta      json.RawMessage `json:"meta"`
	LatencyMs *int            `json:"latency_ms,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	Sources   []Source        `json:"sources,omitempty"`
}
