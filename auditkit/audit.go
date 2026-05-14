// Package auditkit — append-only audit-log: core-абстракция (Entry,
// Recorder) отделена от pgstore-реализации. Кит фиксирует кто/когда/что
// сделал над каким ресурсом (actor_id, action, resource, metadata),
// чтобы compliance и forensics-запросы не приходилось склеивать из
// разрозненных логов.
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
