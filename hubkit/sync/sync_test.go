package sync_test

import (
	"context"
	"encoding/json"
	"sort"
	gosync "sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/hubkit/sync"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// --- фейки портов ---

type feedItem struct {
	ID         string `json:"id"`
	UpdatedSeq int64  `json:"updated_seq"`
}

// fakeFetcher отдаёт items с updated_seq > after, страницами по limit, в порядке
// возрастания seq. Считает вызовы Fetch (для проверки catch-up/идемпотентности).
type fakeFetcher struct {
	items []feedItem
	calls int
}

func (f *fakeFetcher) Fetch(_ context.Context, _ string, after int64, limit int) (sync.Page, error) {
	f.calls++
	sort.Slice(f.items, func(i, j int) bool { return f.items[i].UpdatedSeq < f.items[j].UpdatedSeq })
	var page sync.Page
	for _, it := range f.items {
		if it.UpdatedSeq <= after {
			continue
		}
		raw, _ := json.Marshal(it)
		page.Items = append(page.Items, raw)
		if it.UpdatedSeq > page.NextAfter {
			page.NextAfter = it.UpdatedSeq
		}
		if len(page.Items) >= limit {
			break
		}
	}
	return page, nil
}

// fakeStore — проекция по id (upsert идемпотентен). applies считает все вызовы
// Upsert-элементов (в т.ч. повторные), state — финальное содержимое.
type fakeStore struct {
	mu      gosync.Mutex
	state   map[string]int64
	applies int
}

func newFakeStore() *fakeStore { return &fakeStore{state: map[string]int64{}} }

func (s *fakeStore) Upsert(_ context.Context, _ string, items []json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, raw := range items {
		var it feedItem
		if err := json.Unmarshal(raw, &it); err != nil {
			return err
		}
		s.state[it.ID] = it.UpdatedSeq
		s.applies++
	}
	return nil
}

// memCursor — CursorStore в памяти.
type memCursor struct {
	mu gosync.Mutex
	m  map[string]int64
}

func newMemCursor() *memCursor { return &memCursor{m: map[string]int64{}} }

func (c *memCursor) Load(_ context.Context, resource string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[resource], nil
}

func (c *memCursor) Save(_ context.Context, resource string, cur int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[resource] = cur
	return nil
}

func engine(f sync.Fetcher, st sync.ProjectionStore, cur sync.CursorStore, limit int) *sync.Engine {
	return sync.NewEngine("companies", f, st, cur, sync.Config{Limit: limit, Interval: time.Minute}, logkit.Nop())
}

func TestSync_CatchUp_MultiPage(t *testing.T) {
	f := &fakeFetcher{items: []feedItem{
		{"c1", 1}, {"c2", 2}, {"c3", 3}, {"c4", 4}, {"c5", 5},
	}}
	st := newFakeStore()
	cur := newMemCursor()
	e := engine(f, st, cur, 2)

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(st.state) != 5 {
		t.Fatalf("ожидали 5 сущностей в проекции, получили %d", len(st.state))
	}
	got, _ := cur.Load(context.Background(), "companies")
	if got != 5 {
		t.Fatalf("курсор=%d, ожидали 5", got)
	}
	// 5 записей по 2 на страницу → страницы 2,2,1: последняя короткая, стоп.
	// Плюс не должно быть лишнего Fetch после короткой страницы.
	if f.calls != 3 {
		t.Fatalf("ожидали 3 вызова Fetch (catch-up), получили %d", f.calls)
	}
}

func TestSync_Idempotent_SecondRunNoOp(t *testing.T) {
	f := &fakeFetcher{items: []feedItem{{"c1", 1}, {"c2", 2}, {"c3", 3}}}
	st := newFakeStore()
	cur := newMemCursor()
	e := engine(f, st, cur, 10)

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	appliesAfterFirst := st.applies

	// Второй прогон без новых данных: курсор уже на конце → апсертов нет.
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if st.applies != appliesAfterFirst {
		t.Fatalf("второй прогон применил %d новых записей, ожидали 0", st.applies-appliesAfterFirst)
	}
	if len(st.state) != 3 {
		t.Fatalf("проекция раздулась: %d записей", len(st.state))
	}
}

func TestSync_Idempotent_RedeliveryFromResetCursor(t *testing.T) {
	// Повторная доставка уже применённых записей (курсор сброшен, напр. после
	// восстановления) не плодит дубли — апсерт по id.
	f := &fakeFetcher{items: []feedItem{{"c1", 1}, {"c2", 2}}}
	st := newFakeStore()
	cur := newMemCursor()
	e := engine(f, st, cur, 10)

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	_ = cur.Save(context.Background(), "companies", 0) // сброс курсора → редоставка

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if len(st.state) != 2 {
		t.Fatalf("после редоставки ожидали 2 уникальных записи, получили %d", len(st.state))
	}
}

func TestSync_IncrementalAfterNewData(t *testing.T) {
	f := &fakeFetcher{items: []feedItem{{"c1", 1}, {"c2", 2}}}
	st := newFakeStore()
	cur := newMemCursor()
	e := engine(f, st, cur, 10)

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	// Прилетела новая сущность выше курсора.
	f.items = append(f.items, feedItem{"c3", 3})
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if st.state["c3"] != 3 || len(st.state) != 3 {
		t.Fatalf("инкремент не применён: %+v", st.state)
	}
	got, _ := cur.Load(context.Background(), "companies")
	if got != 3 {
		t.Fatalf("курсор=%d, ожидали 3", got)
	}
}
