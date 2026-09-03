// Package outboxkit — transactional-outbox: Bus вставляет событие в
// outbox-таблицу в той же транзакции, что и бизнес-state; Worker тиком
// (одна транзакция: list → publish → mark) вычитывает unpublished и публикует
// в JetStream через natskit.StreamBus, помечая published только подтверждённые
// PubAck'ом. OUTBOX_PUBLISH_MODE выбирает компромисс: batch (async, best-effort
// порядок, реплики дренируют параллельно) или ordered (строгий порядок, стоп на
// первой ошибке, голову очереди держит один воркер). Гарантия — at-least-once;
// id строки уходит как Nats-Msg-Id, поэтому переотправка на ретрае
// дедуплицируется стримом. OUTBOX_MAX_ATTEMPTS>0 помечает строку dead после N
// неудач; retention партиций — OUTBOX_RETENTION_MONTHS (pgstore). Core-абстракция
// (Repository) отделена от pgstore-реализации, чтобы доменный код не зависел от
// Postgres. Эмитит outbox_events_{inserted,processed,dead}_total,
// outbox_batch_{size,duration_seconds}, outbox_publish_duration_seconds (label
// instance); outbox_lag_seconds и outbox_pending_count — через BacklogReporter.
package outboxkit

import (
	"context"
	"encoding/json"
	"errors"
)

// DefaultInstance — имя single-instance outbox'а (Module): label instance в
// метриках; совпадает с таблицей pgstore.DefaultTable.
const DefaultInstance = "outbox"

// ErrBusy — ordered-режим: голову очереди держит другой воркер, тик пропускается.
var ErrBusy = errors.New("outboxkit: queue head is held by another worker")

type Message struct {
	ID       int64
	Subject  string
	Payload  json.RawMessage
	Attempts int
}

// Failure — результат неудачной публикации события: Dead=true помечает строку
// мёртвой (исчерпан MaxAttempts) — она больше не вычитывается на ретрай.
type Failure struct {
	ID   int64
	Err  string
	Dead bool
}

// Repository — порт хранилища; ListUnpublished идёт внутри транзакции тика и
// держит выбранные строки до её конца (ordered: занятая голова очереди → ErrBusy).
type Repository interface {
	Insert(ctx context.Context, subject string, payload any) error
	ListUnpublished(ctx context.Context, limit int, mode PublishMode) ([]Message, error)
	MarkPublished(ctx context.Context, ids []int64) error
	RecordFailures(ctx context.Context, failures []Failure) error
}
