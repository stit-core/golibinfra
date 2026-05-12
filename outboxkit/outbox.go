package outboxkit

import (
	"context"
	"encoding/json"
)

type Message struct {
	ID      int64
	Subject string
	Payload json.RawMessage
}

type Repository interface {
	Insert(ctx context.Context, subject string, payload any) error
	ListUnpublished(ctx context.Context, limit int) ([]Message, error)
	MarkPublished(ctx context.Context, id int64) error
}
