package logkit

import (
	"context"
	"sync"
)

type Attr func(ctx context.Context) (key string, value any, ok bool)

var (
	attrsMu sync.RWMutex
	attrs   []Attr
)

func RegisterAttr(fn Attr) {
	attrsMu.Lock()
	attrs = append(attrs, fn)
	attrsMu.Unlock()
}

func collectAttrs() []Attr {
	attrsMu.RLock()
	cp := make([]Attr, len(attrs))
	copy(cp, attrs)
	attrsMu.RUnlock()
	return cp
}

func resetAttrs() {
	attrsMu.Lock()
	attrs = nil
	attrsMu.Unlock()
}
