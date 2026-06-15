// Package outboxkit — transactional-outbox: Bus вставляет событие в
// outbox-таблицу в той же транзакции, что и бизнес-state; Worker батчами
// вычитывает unpublished и публикует в JetStream через natskit.StreamBus,
// помечая published (одним batch-update) только подтверждённые PubAck'ом.
// OUTBOX_PUBLISH_MODE выбирает компромисс: batch (async, best-effort порядок)
// или ordered (строгий порядок, стоп на первой ошибке). Гарантия —
// at-least-once; id строки уходит как Nats-Msg-Id, поэтому переотправка на
// ретрае дедуплицируется стримом. Core-абстракция (Repository) отделена от
// pgstore-реализации, чтобы доменный код не зависел от Postgres. Эмитит
// outbox_events_{inserted,processed}_total, outbox_batch_{size,duration_seconds},
// outbox_publish_duration_seconds; outbox_lag_seconds — opt-in через
// LagReporter + RegisterLagGauge.
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
	MarkPublished(ctx context.Context, ids []int64) error
}
