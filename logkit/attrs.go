package logkit

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Attr извлекает значение поля из ctx; ok=false — поле не добавляется в запись.
type Attr func(ctx context.Context) (slog.Value, bool)

type keyedAttr struct {
	key string
	fn  Attr
}

var (
	// Регистрация редка, чтение — на каждой записи, поэтому copy-on-write под
	// mutex и безблокировочный Load на горячем пути.
	attrsMu  sync.Mutex
	attrsPtr atomic.Pointer[[]keyedAttr]
)

// RegisterAttr регистрирует извлечение поля key из ctx для каждой записи;
// повторная регистрация того же ключа заменяет предыдущую.
func RegisterAttr(key string, fn Attr) {
	if key == "" {
		panic("logkit: RegisterAttr: empty key")
	}
	if fn == nil {
		panic("logkit: RegisterAttr: nil func")
	}

	attrsMu.Lock()
	defer attrsMu.Unlock()

	cur := loadAttrs()
	next := make([]keyedAttr, 0, len(cur)+1)
	replaced := false
	for _, a := range cur {
		if a.key == key {
			a.fn = fn
			replaced = true
		}
		next = append(next, a)
	}
	if !replaced {
		next = append(next, keyedAttr{key: key, fn: fn})
	}
	attrsPtr.Store(&next)
}

func loadAttrs() []keyedAttr {
	if p := attrsPtr.Load(); p != nil {
		return *p
	}
	return nil
}

func registeredCtxAttrs(ctx context.Context, dst []slog.Attr) []slog.Attr {
	for _, a := range loadAttrs() {
		if v, ok := a.fn(ctx); ok {
			dst = append(dst, slog.Attr{Key: a.key, Value: v})
		}
	}
	return dst
}

func resetAttrs() { attrsPtr.Store(nil) }
