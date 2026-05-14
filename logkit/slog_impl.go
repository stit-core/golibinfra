package logkit

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	Level string `envconfig:"LOG_LEVEL" default:"info"`
}

// logMessagesCounter — единственный counter per-level. Lazy-init на
// первой записи через newLogCounter(); до этого — nil → no-op в record().
var logMessagesCounter = newLogCounter()

func newLogCounter() metric.Int64Counter {
	c, err := otel.Meter("git.stit.tech/stit/golibinfra/logkit").Int64Counter(
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

// New создаёт OTLP-bound Logger: запись идёт в LoggerProvider, дальше его
// processors/exporters решают куда отправить (OTLP HTTP, stdoutlog, etc).
// stdout-JSON путь — только через [NewWithWriter] для тестов.
func New(cfg Config, lp *sdklog.LoggerProvider) Logger {
	return &slogLogger{l: NewSlog(cfg, lp)}
}

// NewSlog возвращает *slog.Logger c тем же chain, что и [New]. Для consumer'ов
// которые принимают *slog.Logger напрямую (timerkit и пр.).
func NewSlog(cfg Config, lp *sdklog.LoggerProvider) *slog.Logger {
	base := otelslog.NewHandler("backend", otelslog.WithLoggerProvider(lp))
	// На прод-пути trace_id/span_id уже embedded в LogRecord самим otelslog
	// (вычитывает из ctx), service/env/version — в resource attrs telemetry.Init.
	// attrHandler добавляет только user-registered (RegisterAttr) attrs — например,
	// request_id из httpkit middleware.
	return slog.New(&attrHandler{
		Handler: &levelHandler{level: parseLevel(cfg.Level), inner: base},
	})
}

// NewWithWriter — test-only: JSON в произвольный writer, без OTel SDK.
// Прод-путь идёт через [New] с LoggerProvider; этот конструктор
// удерживается для unit-тестов которым LoggerProvider избыточен.
func NewWithWriter(cfg Config, w io.Writer) Logger {
	return &slogLogger{l: NewSlogWithWriter(cfg, w)}
}

// NewSlogWithWriter — test-only, см. [NewWithWriter].
func NewSlogWithWriter(cfg Config, w io.Writer) *slog.Logger {
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

// levelHandler фильтрует записи по slog.Level перед передачей inner.
// otelslog.Handler сам не делает level-cutoff (шлёт всё в SDK), поэтому
// нужен слой выше для consistency с LOG_LEVEL.
type levelHandler struct {
	level slog.Level
	inner slog.Handler
}

func (h *levelHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *levelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, r)
}

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelHandler{level: h.level, inner: h.inner.WithAttrs(attrs)}
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{level: h.level, inner: h.inner.WithGroup(name)}
}

// attrHandler добавляет только user-registered attrs (RegisterAttr).
// trace_id/span_id НЕ инжектит — на OTel-пути их кладёт otelslog, на
// test-пути это делает traceHandler.
type attrHandler struct{ slog.Handler }

func (h *attrHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, fn := range collectAttrs() {
		if k, v, ok := fn(ctx); ok {
			r.AddAttrs(slog.Any(k, v))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h *attrHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &attrHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *attrHandler) WithGroup(name string) slog.Handler {
	return &attrHandler{Handler: h.Handler.WithGroup(name)}
}

// traceHandler — test-only: для NewSlogWithWriter (raw slog.JSONHandler
// trace context из ctx не читает) вручную добавляет trace_id/span_id +
// user-registered attrs.
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
