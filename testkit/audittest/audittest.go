package audittest

import (
	"context"
	"sync"

	"git.stit.tech/akadmin/golibinfra/auditkit"
)

type Recorder struct {
	mu      sync.Mutex
	entries []auditkit.Entry
}

func (r *Recorder) Log(_ context.Context, e auditkit.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return nil
}

func (r *Recorder) Entries() []auditkit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]auditkit.Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

func (r *Recorder) Actions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.Action
	}
	return out
}

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

func (r *Recorder) Last(action string) *auditkit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.entries) - 1; i >= 0; i-- {
		if r.entries[i].Action == action {
			e := r.entries[i]
			return &e
		}
	}
	return nil
}

func (r *Recorder) ByResource(resource string) []auditkit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []auditkit.Entry
	for _, e := range r.entries {
		if e.Resource == resource {
			out = append(out, e)
		}
	}
	return out
}

func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}
