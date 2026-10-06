// Package outboxkit — transactional outbox: Bus пишет событие в таблицу в
// транзакции домена, Worker тиком-транзакцией публикует его в JetStream.
// Эмитит `outbox_*{instance}`; подробности — README.md и docs/ пакета.
package outboxkit

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// DefaultInstance — имя single-instance outbox'а (Module): значение label
// `instance` в метриках; совпадает с таблицей pgstore.DefaultTable.
const DefaultInstance = "outbox"

// ErrBusy — ordered-режим: голову очереди держит другой воркер, тик пропускается.
var ErrBusy = errors.New("outboxkit: queue head is held by another worker")

// ErrNoTx — Insert вызван вне транзакции при включённом tx-strict guard
// (`OUTBOX_REQUIRE_TX`, `pgstore.WithRequireTx`).
var ErrNoTx = errors.New("outboxkit: insert outside transaction")

// ErrConfig — невалидное значение конфига кита; возвращается из Validate.
var ErrConfig = errors.New("outboxkit: invalid config")

// ErrNotSupported — операция не поддержана реализацией Repository (например
// ReviveDead у хранилища без dead-строк).
var ErrNotSupported = errors.New("outboxkit: operation not supported by repository")

// ErrPayloadTooLarge — JSON-тело события больше OUTBOX_MAX_PAYLOAD_BYTES: брокер его не примет.
var ErrPayloadTooLarge = errors.New("outboxkit: payload too large")

// ErrSubjectNotDeclared — subject вне WithSubjects инстанса: его стрим старт не проверял.
var ErrSubjectNotDeclared = errors.New("outboxkit: subject is not declared for the instance")

// ErrUnavailable — брокер недоступен целиком (соединение, JetStream, таймаут PubAck, дедлайн
// тика): Transport оборачивает такую ошибку, и Worker не тратит на неё попытку строки.
var ErrUnavailable = errors.New("outboxkit: broker unavailable")

// Message — строка outbox'а, вычитанная воркером на публикацию.
type Message struct {
	// ID — идентификатор строки; уходит в Nats-Msg-Id.
	ID int64
	// CreatedAt — момент вставки: ключ партиционирования, входит в MessageRef.
	CreatedAt time.Time
	// Subject — subject публикации.
	Subject string
	// Payload — тело события как оно лежит в хранилище.
	Payload json.RawMessage
	// Attempts — число уже случившихся неудачных попыток публикации.
	Attempts int
}

// Ref возвращает адрес строки для MarkPublished/RecordFailures.
func (m Message) Ref() MessageRef { return MessageRef{ID: m.ID, CreatedAt: m.CreatedAt} }

// MessageRef — адрес строки outbox'а: идентификатор и ключ партиционирования,
// чтобы UPDATE шёл по нужной партиции, а не по всем.
type MessageRef struct {
	// ID — идентификатор строки.
	ID int64
	// CreatedAt — момент вставки (значение из Message, прочитанное хранилищем).
	CreatedAt time.Time
}

// Failure — результат неудачной публикации события.
type Failure struct {
	// Ref — адрес строки.
	Ref MessageRef
	// Err — текст ошибки публикации для last_error.
	Err string
	// RetryAfter — пауза до следующей попытки; 0 — без паузы (следующий тик).
	RetryAfter time.Duration
	// Dead — строка исчерпала MaxAttempts и больше не вычитывается на ретрай.
	Dead bool
}

// ReviveFilter — отбор dead-строк для ReviveDead. Пустой фильтр берёт все
// dead-строки; заданные поля соединяются по И.
type ReviveFilter struct {
	// IDs — идентификаторы строк; пусто — без ограничения по id.
	IDs []int64
	// Subject — subject строки; пусто — без ограничения по subject.
	Subject string
	// Since — нижняя граница created_at строки; нулевое время — без границы.
	Since time.Time
}

// Repository — порт хранилища; ListUnpublished идёт внутри транзакции тика и
// держит выбранные строки до её конца (ordered: занятая голова очереди → ErrBusy).
type Repository interface {
	Insert(ctx context.Context, subject string, payload json.RawMessage) error
	ListUnpublished(ctx context.Context, limit int, mode PublishMode) ([]Message, error)
	MarkPublished(ctx context.Context, refs []MessageRef) error
	RecordFailures(ctx context.Context, failures []Failure) error
}

// DeadReviver — опциональный интерфейс Repository: возврат dead-строк в очередь
// (реализует pgstore.Store). Через Outbox.ReviveDead становится
// `outbox_revived_total`.
type DeadReviver interface {
	ReviveDead(ctx context.Context, filter ReviveFilter) (int64, error)
}

// Maintainer — опциональный интерфейс Repository: периодические job'ы
// обслуживания хранилища (партиции, retention). Instance кладёт их в
// fx-группу timerkit.JobsGroup рядом с job'ом воркера.
type Maintainer interface {
	MaintenanceJobs() []timerkit.Job
}

// RepositoryFactory строит хранилище и схему именованной таблицы для Instance;
// провайдит store-кит (`pgstore.FactoryModule`).
type RepositoryFactory interface {
	// Repository — хранилище поверх таблицы table.
	Repository(table string) (Repository, error)
	// Migrations — источник миграций таблицы table; Instance кладёт его в
	// группу pgkit.MigrationsGroup.
	Migrations(table string) pgkit.Source
}
