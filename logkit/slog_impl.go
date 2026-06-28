package logkit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	Level string `envconfig:"LOG_LEVEL" default:"info"`
	// Stdout по умолчанию через envconfig=true; в struct-литерале zero-value
	// false — задавайте явно, как у telemetry.Config.OTLPInsecure.
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

// New создаёт Logger с fan-out: запись идёт в OTel LoggerProvider (OTLP →
// Loki) и, при cfg.Stdout, в JSON-stdout. Метрики/трейсы в stdout не идут.
func New(cfg Config, lp *sdklog.LoggerProvider) Logger {
	return &slogLogger{l: NewSlog(cfg, lp)}
}

// NewSlog возвращает *slog.Logger c тем же chain, что и [New]. Для consumer'ов
// которые принимают *slog.Logger напрямую (timerkit и пр.).
func NewSlog(cfg Config, lp *sdklog.LoggerProvider) *slog.Logger {
	return newSlog(cfg, lp, os.Stdout)
}

// newSlog — общий конструктор chain'а; stdoutW параметризован для тестов.
func newSlog(cfg Config, lp *sdklog.LoggerProvider, stdoutW io.Writer) *slog.Logger {
	level := parseLevel(cfg.Level)

	// otelslog сам embed'ит trace_id/span_id из ctx; service/env/version — в
	// resource attrs telemetry.Init. attrHandler добавляет только
	// user-registered (RegisterAttr) attrs, например request_id из httpkit.
	sinks := []slog.Handler{
		&attrHandler{Handler: otelslog.NewHandler("backend", otelslog.WithLoggerProvider(lp))},
	}
	if cfg.Stdout {
		// raw JSONHandler trace context из ctx не читает — traceHandler
		// добавляет trace_id/span_id + те же RegisterAttr attrs.
		sinks = append(sinks, &traceHandler{
			Handler: slog.NewJSONHandler(stdoutW, &slog.HandlerOptions{Level: level}),
		})
	}

	return slog.New(&levelHandler{level: level, inner: fanoutHandler{handlers: sinks}})
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

// fanoutHandler рассылает запись по всем sink'ам (OTLP + stdout). Record
// клонируется на каждый sink: AddAttrs (trace_id в traceHandler) не должен
// течь в соседний sink через общий backing-массив attrs.
type fanoutHandler struct {
	handlers []slog.Handler
}

func (h fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, hh := range h.handlers {
		if !hh.Enabled(ctx, r.Level) {
			continue
		}
		if err := hh.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		next[i] = hh.WithAttrs(attrs)
	}
	return fanoutHandler{handlers: next}
}

func (h fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		next[i] = hh.WithGroup(name)
	}
	return fanoutHandler{handlers: next}
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
