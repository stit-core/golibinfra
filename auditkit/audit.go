// Package auditkit — append-only audit-log: core-абстракция (Entry,
// Logger) отделена от pgstore-реализации. Кит фиксирует кто/когда/что
// сделал над каким ресурсом (actor_id, action, resource, metadata),
// чтобы compliance и forensics-запросы не приходилось склеивать из
// разрозненных логов.
package auditkit

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidEntry — запись не проходит Entry.Validate: пустое обязательное поле
// или значение длиннее предела кита.
var ErrInvalidEntry = errors.New("auditkit: invalid entry")

// Предельные длины полей, попадающих в btree-индексы audit_log: значение длиннее
// не помещается в индексную запись Postgres, и INSERT падает уже в базе.
const (
	// MaxActorIDLen — предел длины Entry.ActorID.
	MaxActorIDLen = 255
	// MaxActionLen — предел длины Entry.Action.
	MaxActionLen = 255
	// MaxResourceLen — предел длины Entry.Resource.
	MaxResourceLen = 255
	// MaxResourceIDLen — предел длины Entry.ResourceID.
	MaxResourceIDLen = 255
)

// Entry — одна запись аудита: кто (ActorID), что сделал (Action) над каким
// ресурсом (Resource, ResourceID) и когда (At).
type Entry struct {
	// ActorID — идентификатор инициатора (пользователь, сервис); пусто → NULL.
	ActorID string
	// Action — что сделано, например `user.create`; обязательно.
	Action string
	// Resource — тип ресурса, например `user`; обязательно.
	Resource string
	// ResourceID — идентификатор конкретного ресурса; пусто → NULL.
	ResourceID string
	// Metadata — структурированный контекст события; пусто → NULL.
	Metadata map[string]any
	// At — момент события; нулевое значение → часы базы (NOW()).
	At time.Time
	// IP — адрес инициатора; пусто → NULL.
	IP string
	// RequestID — корреляция с HTTP/RPC-запросом; пусто → NULL.
	RequestID string
}

// Validate проверяет обязательные поля и предельные длины; нарушение —
// ErrInvalidEntry. Logger вызывает её до записи.
func (e Entry) Validate() error {
	switch {
	case e.Action == "":
		return fmt.Errorf("%w: Action is required", ErrInvalidEntry)
	case e.Resource == "":
		return fmt.Errorf("%w: Resource is required", ErrInvalidEntry)
	case len(e.ActorID) > MaxActorIDLen:
		return fmt.Errorf("%w: ActorID is longer than %d bytes", ErrInvalidEntry, MaxActorIDLen)
	case len(e.Action) > MaxActionLen:
		return fmt.Errorf("%w: Action is longer than %d bytes", ErrInvalidEntry, MaxActionLen)
	case len(e.Resource) > MaxResourceLen:
		return fmt.Errorf("%w: Resource is longer than %d bytes", ErrInvalidEntry, MaxResourceLen)
	case len(e.ResourceID) > MaxResourceIDLen:
		return fmt.Errorf("%w: ResourceID is longer than %d bytes", ErrInvalidEntry, MaxResourceIDLen)
	}
	return nil
}

// Logger — порт записи аудита; реализация в библиотеке — auditkit/pgstore.
type Logger interface {
	// Log пишет одну запись; невалидная Entry — ErrInvalidEntry.
	Log(ctx context.Context, e Entry) error
}
