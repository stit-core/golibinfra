package logkit

import (
	"context"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	Level string `envconfig:"LOG_LEVEL" default:"info"`
	// Stdout по умолчанию через envconfig=true; в struct-литерале zero-value
	// false — задавайте явно. Единственный sink logger'а — JSON в stdout;
	// при Stdout=false запись отбрасывается (io.Discard).
	Stdout bool `envconfig:"LOG_STDOUT" default:"true"`
}

// logMessagesCounter — единственный counter per-level. Lazy-init на
// первой записи через newLogCounter(); до этого — nil → no-op в record().
var logMessagesCounter = newLogCounter()

func newLogCounter() metric.Int64Counter {
	c, err := otel.Meter("git.stit.tech/stit-core/golibinfra/logkit").Int64Counter(
		"log_messages_total",
		metric.WithDescription("Log messages by level"),
	)
	if err != nil {
		return nil
	}
	return c
}

func recordLog(ctx context.Context, level string) {
	if logMessagesCounter == nil {
		return
	}
	logMessagesCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("level", level)))
}

type slogLogger struct {
	l *slog.Logger
}

// New создаёт Logger, пишущий JSON-записи в stdout (при cfg.Stdout;
// иначе — в io.Discard). trace_id/span_id берутся из ctx. Метрики/трейсы
// сюда не идут — это только log pipeline.
func New(cfg Config) Logger {
	return &slogLogger{l: NewSlog(cfg)}
}

// NewSlog возвращает *slog.Logger c тем же chain, что и [New]. Для consumer'ов
// которые принимают *slog.Logger напрямую (timerkit и пр.).
func NewSlog(cfg Config) *slog.Logger {
	return newSlog(cfg, os.Stdout)
}

// newSlog — общий конструктор chain'а; stdoutW параметризован для тестов.
// При cfg.Stdout=false запись отбрасывается в io.Discard.
func newSlog(cfg Config, stdoutW io.Writer) *slog.Logger {
	w := stdoutW
	if !cfg.Stdout {
		w = io.Discard
	}
	return buildSlog(cfg, w)
}

// NewWithWriter — JSON в произвольный writer (stdout/file), без gating по
// cfg.Stdout: явный writer всегда получает записи. Используется для
// file-sink и unit-тестов.
func NewWithWriter(cfg Config, w io.Writer) Logger {
	return &slogLogger{l: NewSlogWithWriter(cfg, w)}
}

// NewSlogWithWriter — *slog.Logger вариант [NewWithWriter].
func NewSlogWithWriter(cfg Config, w io.Writer) *slog.Logger {
	return buildSlog(cfg, w)
}

// buildSlog собирает единственный sink: JSON-writer, обёрнутый traceHandler
// (trace_id/span_id из ctx + user-registered RegisterAttr attrs). Level-cutoff
// делает сам JSONHandler через HandlerOptions.Level.
func buildSlog(cfg Config, w io.Writer) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(cfg.Level)})
	return slog.New(&traceHandler{Handler: base})
}

func parseLevel(s string) slog.Level {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return lvl
}

func (s *slogLogger) Debug(ctx context.Context, msg string, kv ...any) {
	recordLog(ctx, "debug")
	s.l.DebugContext(ctx, msg, kv...)
}

func (s *slogLogger) Info(ctx context.Context, msg string, kv ...any) {
	recordLog(ctx, "info")
	s.l.InfoContext(ctx, msg, kv...)
}

func (s *slogLogger) Warn(ctx context.Context, msg string, kv ...any) {
	recordLog(ctx, "warn")
	s.l.WarnContext(ctx, msg, kv...)
}

func (s *slogLogger) Error(ctx context.Context, msg string, kv ...any) {
	recordLog(ctx, "error")
	s.l.ErrorContext(ctx, msg, kv...)
}

func (s *slogLogger) With(kv ...any) Logger {
	return &slogLogger{l: s.l.With(kv...)}
}

// traceHandler добавляет к каждой записи trace_id/span_id из ctx (raw
// slog.JSONHandler trace context не читает) и user-registered attrs
// (RegisterAttr, например request_id из httpkit).
type traceHandler struct{ slog.Handler }

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	for _, fn := range collectAttrs() {
		if k, v, ok := fn(ctx); ok {
			r.AddAttrs(slog.Any(k, v))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithGroup(name)}
}
