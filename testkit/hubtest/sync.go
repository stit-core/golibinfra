package hubtest

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	hubsync "git.stit.tech/stit-core/golibinfra/hubkit/sync"
)

// Feed — фид одного ресурса в памяти: реализует hubkit/sync.Fetcher, отдавая
// элементы страницами по limit в порядке добавления.
type Feed struct {
	mu    sync.Mutex
	items map[string][]feedItem
	err   map[string]error
	calls int
}

type feedItem struct {
	seq int64
	raw json.RawMessage
}

// NewFeed — пустой фид.
func NewFeed() *Feed {
	return &Feed{items: map[string][]feedItem{}, err: map[string]error{}}
}

// Add кладёт сущность в фид ресурса под курсором seq (updated_seq элемента).
func (f *Feed) Add(t testing.TB, resource string, seq int64, entity any) {
	t.Helper()
	raw, err := json.Marshal(entity)
	if err != nil {
		t.Fatalf("hubtest: marshal feed item: %v", err)
	}
	f.AddRaw(resource, seq, raw)
}

// AddRaw кладёт готовый JSON сущности в фид ресурса.
func (f *Feed) AddRaw(resource string, seq int64, raw json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[resource] = append(f.items[resource], feedItem{seq: seq, raw: raw})
}

// FailWith заставляет Fetch ресурса возвращать ошибку (проверка error-веток тика);
// nil снимает отказ.
func (f *Feed) FailWith(resource string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err[resource] = err
}

// Calls — сколько раз движок ходил за страницей.
func (f *Feed) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Fetch — реализация hubkit/sync.Fetcher.
func (f *Feed) Fetch(_ context.Context, resource string, after int64, limit int) (hubsync.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := f.err[resource]; err != nil {
		return hubsync.Page{}, err
	}
	page := hubsync.Page{NextAfter: after}
	for _, it := range f.items[resource] {
		if it.seq <= after {
			continue
		}
		page.Items = append(page.Items, it.raw)
		page.NextAfter = it.seq
		if len(page.Items) == limit {
			break
		}
	}
	return page, nil
}

// Projection — hubkit/sync.ProjectionStore в памяти: хранит применённые батчи
// по ресурсам.
type Projection struct {
	mu      sync.Mutex
	batches map[string][]json.RawMessage
	err     error
}

// NewProjection — пустая проекция.
func NewProjection() *Projection { return &Projection{batches: map[string][]json.RawMessage{}} }

// FailWith заставляет Upsert возвращать ошибку; nil снимает отказ.
func (p *Projection) FailWith(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// Upsert — реализация hubkit/sync.ProjectionStore.
func (p *Projection) Upsert(_ context.Context, resource string, items []json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.batches[resource] = append(p.batches[resource], items...)
	return nil
}

// Items — всё, что движок применил по ресурсу (в порядке применения).
func (p *Projection) Items(resource string) []json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]json.RawMessage(nil), p.batches[resource]...)
}

// Cursors — hubkit/sync.CursorStore в памяти: неизвестный ресурс — курсор 0
// (как маппинг ErrNoRows у продукта).
type Cursors struct {
	mu       sync.Mutex
	values   map[string]int64
	loadErr  error
	saveErr  error
	saveCall int
}

// NewCursors — пустое хранилище курсоров.
func NewCursors() *Cursors { return &Cursors{values: map[string]int64{}} }

// Set задаёт курсор ресурса до старта тика.
func (c *Cursors) Set(resource string, cursor int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[resource] = cursor
}

// FailLoad заставляет Load возвращать err (nil — снять отказ).
func (c *Cursors) FailLoad(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadErr = err
}

// FailSave заставляет Save возвращать ошибку; nil снимает отказ.
func (c *Cursors) FailSave(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saveErr = err
}

// Load — реализация hubkit/sync.CursorStore.
func (c *Cursors) Load(_ context.Context, resource string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loadErr != nil {
		return 0, c.loadErr
	}
	return c.values[resource], nil
}

// Save — реализация hubkit/sync.CursorStore.
func (c *Cursors) Save(_ context.Context, resource string, cursor int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saveCall++
	if c.saveErr != nil {
		return c.saveErr
	}
	c.values[resource] = cursor
	return nil
}

// Cursor — текущий курсор ресурса.
func (c *Cursors) Cursor(resource string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[resource]
}

// Saves — сколько раз движок продвигал курсор.
func (c *Cursors) Saves() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveCall
}
