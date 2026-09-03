// Package sync — движок pull-синка локальных проекций хаба.
//
// Каждый Engine на каждый тик догоняет change-feed одного ресурса
// (companies/projects/employees/visibility): читает страницами
// `after=<cursor>&limit=`, апсертит сырые сущности в проекцию через порт
// ProjectionStore, продвигает курсор через CursorStore. Идемпотентно (апсерт по
// id + монотонный курсор) — catch-up после простоя автоматический. Метрики: lag
// (время с последнего успеха), last-success, курсор, счётчики записей/ошибок.
// Регистрируется как timerkit.Job (graceful shutdown через fx-lifecycle timerkit).
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

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
// Реализует продукт поверх своей БД.
type CursorStore interface {
	Load(ctx context.Context, resource string) (int64, error)
	Save(ctx context.Context, resource string, cursor int64) error
}

// Engine синкает один ресурс. Собирается на сервисе по одному на каждый ресурс.
type Engine struct {
	resource string
	fetcher  Fetcher
	store    ProjectionStore
	cursor   CursorStore
	limit    int
	interval time.Duration
	log      logkit.Logger
	instr    *syncInstruments

	lastSuccessUnix atomic.Int64
	cursorValue     atomic.Int64
}

// NewEngine строит движок для ресурса. limit/interval берутся из cfg.
func NewEngine(resource string, fetcher Fetcher, store ProjectionStore, cursor CursorStore, cfg Config, log logkit.Logger) *Engine {
	e := &Engine{
		resource: resource,
		fetcher:  fetcher,
		store:    store,
		cursor:   cursor,
		limit:    cfg.Limit,
		interval: cfg.Interval,
		log:      log.With("component", "hubsync", "resource", resource),
		instr:    newSyncInstruments(resource),
	}
	e.instr.registerObservables(e)
	return e
}

// Run — один тик: догнать фид до конца (catch-up внутри тика). Идемпотентен и
// безопасен к повторному запуску: курсор монотонен, апсерт дедуплицирует.
func (e *Engine) Run(ctx context.Context) error {
	cur, err := e.cursor.Load(ctx, e.resource)
	if err != nil {
		e.instr.recordError(ctx)
		return fmt.Errorf("hubkit/sync: load cursor %s: %w", e.resource, err)
	}

	for {
		page, err := e.fetcher.Fetch(ctx, e.resource, cur, e.limit)
		if err != nil {
			e.instr.recordError(ctx)
			return fmt.Errorf("hubkit/sync: fetch %s after=%d: %w", e.resource, cur, err)
		}
		if len(page.Items) == 0 {
			break
		}

		if err := e.store.Upsert(ctx, e.resource, page.Items); err != nil {
			e.instr.recordError(ctx)
			return fmt.Errorf("hubkit/sync: upsert %s: %w", e.resource, err)
		}

		next := nextCursor(cur, page)
		if next <= cur {
			// Защита от зацикливания: фид не продвинул курсор, хотя вернул items.
			e.log.Warn(ctx, "sync cursor did not advance", "cursor", cur, "items", len(page.Items))
			break
		}
		cur = next
		if err := e.cursor.Save(ctx, e.resource, cur); err != nil {
			e.instr.recordError(ctx)
			return fmt.Errorf("hubkit/sync: save cursor %s: %w", e.resource, err)
		}
		e.instr.recordRecords(ctx, len(page.Items))
		e.cursorValue.Store(cur)

		if len(page.Items) < e.limit {
			break // короткая страница — фид исчерпан, догнали
		}
	}

	e.lastSuccessUnix.Store(time.Now().Unix())
	return nil
}

// Job оборачивает Engine в timerkit.Job для регистрации в group `timer.job`.
func (e *Engine) Job() timerkit.Job {
	return timerkit.Job{
		Name:     "hubsync." + e.resource,
		Interval: e.interval,
		Run:      e.Run,
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
