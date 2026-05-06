package auditkit

import (
	"context"
	"time"
)

type Entry struct {
	ActorID    string
	Action     string
	Resource   string
	ResourceID string
	Metadata   map[string]any
	At         time.Time
	IP         string
	RequestID  string
}

type Logger interface {
	Log(ctx context.Context, e Entry) error
}
