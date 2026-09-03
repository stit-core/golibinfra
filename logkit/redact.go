package logkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
)

// Дефолтные паттерны — эвристики компактных форм: email, телефон (E.164 с «+»
// либо RU-номер 7/8 + 10 цифр), карта (4×4 цифры). Свои — LOG_REDACT_PATTERNS.
var defaultRedactPatterns = Patterns{
	`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`,
	`\+\d{10,15}\b|\b[78]\d{10}\b`,
	`\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`,
}

type redactHandler struct {
	inner slog.Handler
	res   []*regexp.Regexp
	with  string
}

// NewRedactHandler маскирует msg и строковые значения атрибутов (включая
// With()-attrs, группы, LogValuer, error и JSON-представление Any-значений);
// числа, bool, time и duration не трогает.
func NewRedactHandler(inner slog.Handler, cfg RedactConfig) (slog.Handler, error) {
	patterns := cfg.Patterns
	if len(patterns) == 0 {
		patterns = defaultRedactPatterns
	}
	res := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("%w: LOG_REDACT_PATTERNS %q: %w", ErrConfig, p, err)
		}
		res = append(res, re)
	}
	with := cfg.ReplaceWith
	if with == "" {
		with = "[REDACTED]"
	}
	return &redactHandler{inner: inner, res: res, with: with}, nil
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.text(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = h.attr(a)
	}
	return &redactHandler{inner: h.inner.WithAttrs(out), res: h.res, with: h.with}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name), res: h.res, with: h.with}
}

func (h *redactHandler) text(s string) string {
	for _, re := range h.res {
		s = re.ReplaceAllString(s, h.with)
	}
	return s
}

func (h *redactHandler) bytes(b []byte) []byte {
	for _, re := range h.res {
		b = re.ReplaceAll(b, []byte(h.with))
	}
	return b
}

func (h *redactHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.text(v.String()))
	case slog.KindGroup:
		in := v.Group()
		out := make([]slog.Attr, len(in))
		for i, g := range in {
			out[i] = h.attr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		return h.anyAttr(a.Key, v.Any())
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}

// anyAttr: error — как строка (так его рендерит и JSONHandler); прочее — через
// JSON, и только если маска что-то заменила, значение подменяется RawMessage.
func (h *redactHandler) anyAttr(key string, x any) slog.Attr {
	if err, ok := x.(error); ok {
		if _, isMarshaler := x.(json.Marshaler); !isMarshaler {
			return slog.String(key, h.text(err.Error()))
		}
	}
	b, err := json.Marshal(x)
	if err != nil {
		return slog.Any(key, x)
	}
	if rb := h.bytes(b); !bytes.Equal(rb, b) {
		return slog.Any(key, json.RawMessage(rb))
	}
	return slog.Any(key, x)
}
