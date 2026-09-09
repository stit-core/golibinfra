// Package audittest — фейк auditkit.Logger для юнит-тестов домена без Postgres.
package audittest

import (
	"context"
	"maps"
	"slices"
	"sync"

	"git.stit.tech/stit-core/golibinfra/auditkit"
)

// Recorder — in-memory auditkit.Logger: копит записи и отвечает на запросы теста.
type Recorder struct {
	mu      sync.Mutex
	entries []auditkit.Entry
}

// Log сохраняет копию записи; невалидная Entry отвергается так же, как
// в pgstore — auditkit.ErrInvalidEntry.
func (r *Recorder) Log(_ context.Context, e auditkit.Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	// Копия Metadata: caller волен переиспользовать свою map после Log,
	// pgstore к этому моменту уже смаршалил её в JSON.
	e.Metadata = maps.Clone(e.Metadata)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return nil
}

// Entries — копия всех записей в порядке добавления.
func (r *Recorder) Entries() []auditkit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]auditkit.Entry, len(r.entries))
	for i, e := range r.entries {
		out[i] = cloneEntry(e)
	}
	return out
}

// Actions — действия всех записей в порядке добавления.
func (r *Recorder) Actions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.Action
	}
	return out
}

// Has сообщает, была ли запись с таким действием.
func (r *Recorder) Has(action string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

// Last — копия последней записи с таким действием либо nil.
func (r *Recorder) Last(action string) *auditkit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range slices.Backward(r.entries) {
		if v.Action == action {
			e := cloneEntry(v)
			return &e
		}
	}
	return nil
}

// ByResource — записи по ресурсу в порядке добавления.
func (r *Recorder) ByResource(resource string) []auditkit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []auditkit.Entry
	for _, e := range r.entries {
		if e.Resource == resource {
			out = append(out, cloneEntry(e))
		}
	}
	return out
}

func cloneEntry(e auditkit.Entry) auditkit.Entry {
	e.Metadata = maps.Clone(e.Metadata)
	return e
}

// Reset очищает накопленные записи.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}
