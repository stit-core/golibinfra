// Package sync — движок pull-синка локальных проекций хаба: тик догоняет
// change-feed ресурса страницами `after=<cursor>` через порты продукта.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Page — страница change-feed'а: сырые сущности (в порядке возрастания
// updated_seq) + курсор следующей страницы.
type Page struct {
	Items     []json.RawMessage
	NextAfter int64
}

// Fetcher тянет одну страницу фида ресурса после курсора after. Реализуется
// адаптером HTTPFetcher поверх hubkit/client (или моком в тестах).
type Fetcher interface {
	Fetch(ctx context.Context, resource string, after int64, limit int) (Page, error)
}

// ProjectionStore применяет батч сырых сущностей в локальную проекцию. Реализует
// продукт (обычно INSERT … ON CONFLICT DO UPDATE — апсерт по id, идемпотентно).
type ProjectionStore interface {
	Upsert(ctx context.Context, resource string, items []json.RawMessage) error
}

// CursorStore хранит курсор синка per-resource (таблица hub_sync_cursors).
// Реализует продукт поверх своей БД; отсутствие строки ресурса — курсор 0.
type CursorStore interface {
	Load(ctx context.Context, resource string) (int64, error)
	Save(ctx context.Context, resource string, cursor int64) error
}

// Option — настройка движка, передаётся в NewEngine.
type Option func(*options)

type options struct {
	clock clockkit.Clock
}

// WithClock подменяет часы отметки последнего успеха (детерминированные тесты).
func WithClock(clock clockkit.Clock) Option {
	return func(o *options) {
		if clock != nil {
			o.clock = clock
		}
	}
}

// Engine синкает один ресурс. Собирается на сервисе по одному на каждый ресурс.
type Engine struct {
	resource string
	fetcher  Fetcher
	store    ProjectionStore
	cursor   CursorStore
	cfg      Config
	clock    clockkit.Clock
	log      logkit.Logger
	instr    *syncInstruments

	metricsReg metric.Registration
	closed     atomic.Bool

	lastSuccessUnix atomic.Int64
	cursorValue     atomic.Int64
	cursorKnown     atomic.Bool
}

// NewEngine строит движок для ресурса; конфиг валидируется (ErrConfig).
func NewEngine(resource string, fetcher Fetcher, store ProjectionStore, cursor CursorStore, cfg Config, log logkit.Logger, opts ...Option) (*Engine, error) {
	if resource == "" {
		return nil, fmt.Errorf("%w: resource is required", ErrConfig)
	}
	if fetcher == nil || store == nil || cursor == nil {
		return nil, fmt.Errorf("%w: fetcher, store and cursor store are required", ErrConfig)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	o := options{clock: clockkit.Real{}}
	for _, apply := range opts {
		apply(&o)
	}
	e := &Engine{
		resource: resource,
		fetcher:  fetcher,
		store:    store,
		cursor:   cursor,
		cfg:      cfg,
		clock:    o.clock,
		log:      log.With("component", "hubsync", "resource", resource),
		instr:    newSyncInstruments(resource),
	}
	e.metricsReg = e.instr.registerObservables(e)
	return e, nil
}

// Resource — имя ресурса, который синкает движок.
func (e *Engine) Resource() string { return e.resource }

// Close снимает observable-callback движка. Идемпотентен; Module вызывает его
// на OnStop приложения. Без него meter держал бы движок вечно и продолжал
// публиковать hub_sync_cursor_seq / hub_sync_lag_seconds ресурса, который
// больше не тикает.
func (e *Engine) Close() error {
	if !e.closed.CompareAndSwap(false, true) {
		return nil
	}
	return e.metricsReg.Unregister()
}

// Run — один тик: догнать фид до конца (catch-up внутри тика). Идемпотентен и
// безопасен к повторному запуску: курсор монотонен, апсерт дедуплицирует.
// Отмена ctx (graceful shutdown) — не ошибка тика: работа продолжится со
// следующего запуска от сохранённого курсора.
func (e *Engine) Run(ctx context.Context) error {
	err := e.catchUp(ctx)
	switch {
	case err == nil:
		e.lastSuccessUnix.Store(e.clock.Now().Unix())
		return nil
	case errors.Is(ctx.Err(), context.Canceled):
		// Graceful stop посреди тика — не сбой синка: курсор сохранён, catch-up
		// продолжится со следующего запуска. Дедлайн тика (Timeout) — сбой.
		e.log.Debug(ctx, "sync tick cancelled", "err", err)
		return nil
	default:
		e.instr.recordError(ctx)
		return err
	}
}

func (e *Engine) catchUp(ctx context.Context) error {
	cur, err := e.cursor.Load(ctx, e.resource)
	if err != nil {
		return fmt.Errorf("hubkit/sync: load cursor %s: %w", e.resource, err)
	}
	// Курсор в gauge — сразу после Load: на догнанном фиде цикл ниже не делает
	// ни одной итерации, и метрика показывала бы 0 при живом курсоре в БД.
	e.observeCursor(cur)

	for {
		page, err := e.fetcher.Fetch(ctx, e.resource, cur, e.cfg.Limit)
		if err != nil {
			return fmt.Errorf("hubkit/sync: fetch %s after=%d: %w", e.resource, cur, err)
		}
		if len(page.Items) == 0 {
			return nil
		}
		if err := e.store.Upsert(ctx, e.resource, page.Items); err != nil {
			return fmt.Errorf("hubkit/sync: upsert %s: %w", e.resource, err)
		}

		next := nextCursor(cur, page)
		if next <= cur {
			// Защита от зацикливания: фид не продвинул курсор, хотя вернул items.
			e.log.Warn(ctx, "sync cursor did not advance", "cursor", cur, "items", len(page.Items))
			return nil
		}
		cur = next
		if err := e.cursor.Save(ctx, e.resource, cur); err != nil {
			return fmt.Errorf("hubkit/sync: save cursor %s: %w", e.resource, err)
		}
		e.instr.recordRecords(ctx, len(page.Items))
		e.observeCursor(cur)

		if len(page.Items) < e.cfg.Limit {
			return nil // короткая страница — фид исчерпан, догнали
		}
	}
}

func (e *Engine) observeCursor(cur int64) {
	e.cursorValue.Store(cur)
	e.cursorKnown.Store(true)
}

// Job оборачивает Engine в timerkit.Job (группа timerkit.JobsGroup).
func (e *Engine) Job() timerkit.Job {
	return timerkit.Job{
		Name:       "hubsync." + e.resource,
		Interval:   e.cfg.Interval,
		Timeout:    e.cfg.Timeout,
		Jitter:     e.cfg.Jitter,
		RunOnStart: e.cfg.RunOnStart,
		Run:        e.Run,
	}
}

// nextCursor выбирает курсор следующей страницы: явный next_after от хаба, иначе
// максимальный updated_seq в батче (fallback, если фид его не прислал).
func nextCursor(cur int64, page Page) int64 {
	if page.NextAfter > cur {
		return page.NextAfter
	}
	maxSeq := cur
	for _, raw := range page.Items {
		var probe struct {
			UpdatedSeq int64 `json:"updated_seq"`
		}
		if err := json.Unmarshal(raw, &probe); err == nil && probe.UpdatedSeq > maxSeq {
			maxSeq = probe.UpdatedSeq
		}
	}
	return maxSeq
}
