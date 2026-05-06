package logkit

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"regexp"
)

type RedactConfig struct {
	Patterns    []string `envconfig:"LOG_REDACT_PATTERNS"`
	ReplaceWith string   `default:"[REDACTED]"`
	Enabled     bool     `envconfig:"LOG_REDACT_ENABLED"`
}

var defaultRedactPatterns = []string{
	`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`,
	`\+?[0-9]{10,15}`,
	`\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`,
}

type redactHandler struct {
	slog.Handler
	regexps     []*regexp.Regexp
	replaceWith []byte
}

func NewRedactHandler(inner slog.Handler, cfg RedactConfig) slog.Handler {
	patterns := cfg.Patterns
	if len(patterns) == 0 {
		patterns = defaultRedactPatterns
	}
	with := cfg.ReplaceWith
	if with == "" {
		with = "[REDACTED]"
	}
	var regexps []*regexp.Regexp
	for _, p := range patterns {
		r, err := regexp.Compile(p)
		if err == nil {
			regexps = append(regexps, r)
		}
	}
	return &redactHandler{
		Handler:     inner,
		regexps:     regexps,
		replaceWith: []byte(with),
	}
}

func (h *redactHandler) redact(b []byte) []byte {
	for _, re := range h.regexps {
		b = re.ReplaceAll(b, h.replaceWith)
	}
	return b
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	var buf bytes.Buffer
	tmp := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	if err := tmp.Handle(ctx, r); err != nil {
		return err
	}
	redacted := h.redact(buf.Bytes())
	redacted = bytes.TrimRight(redacted, "\n")

	newRecord := slog.NewRecord(r.Time, r.Level, string(redacted), r.PC)
	return h.Handler.Handle(ctx, newRecord)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &redactHandler{
		Handler:     h.Handler.WithAttrs(attrs),
		regexps:     h.regexps,
		replaceWith: h.replaceWith,
	}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{
		Handler:     h.Handler.WithGroup(name),
		regexps:     h.regexps,
		replaceWith: h.replaceWith,
	}
}

func NewWithRedact(cfg Config, redactCfg RedactConfig, w io.Writer) Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(cfg.Level)); err != nil {
		lvl = slog.LevelInfo
	}
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	inner := &traceHandler{Handler: base}
	var handler slog.Handler = inner
	if redactCfg.Enabled {
		handler = NewRedactHandler(inner, redactCfg)
	}
	return &slogLogger{l: slog.New(handler)}
}
