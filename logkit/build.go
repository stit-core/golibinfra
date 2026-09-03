package logkit

import (
	"context"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

var logMessagesCounter = otelkit.Must(otelkit.Meter("logkit").Int64Counter(
	"log_messages_total",
	metric.WithDescription("Log messages by level"),
))

func recordLog(ctx context.Context, level string) {
	logMessagesCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("level", level)))
}

type Option func(*options)

type options struct {
	redact  RedactConfig
	otel    log.LoggerProvider
	service *ServiceConfig
}

// WithRedact включает PII-redact по RedactConfig (при cfg.Enabled).
func WithRedact(cfg RedactConfig) Option {
	return func(o *options) { o.redact = cfg }
}

// WithOTel добавляет второй sink: каждая запись уходит и в OTel LoggerProvider
// (OTLP). nil — sink не добавляется.
func WithOTel(lp log.LoggerProvider) Option {
	return func(o *options) { o.otel = lp }
}

// WithService добавляет статические поля service/env/version в каждую запись.
func WithService(cfg ServiceConfig) Option {
	return func(o *options) { o.service = &cfg }
}

// New — JSON в stdout при cfg.Stdout, иначе JSON-sink отбрасывает записи.
func New(cfg Config, opts ...Option) (Logger, error) {
	l, err := NewSlog(cfg, opts...)
	if err != nil {
		return nil, err
	}
	return &slogLogger{l: l}, nil
}

func NewSlog(cfg Config, opts ...Option) (*slog.Logger, error) {
	var w io.Writer = os.Stdout
	if !cfg.Stdout {
		w = io.Discard
	}
	return NewSlogWithWriter(cfg, w, opts...)
}

// NewWithWriter — JSON в произвольный writer без gating по cfg.Stdout.
func NewWithWriter(cfg Config, w io.Writer, opts ...Option) (Logger, error) {
	l, err := NewSlogWithWriter(cfg, w, opts...)
	if err != nil {
		return nil, err
	}
	return &slogLogger{l: l}, nil
}

func NewSlogWithWriter(cfg Config, w io.Writer, opts ...Option) (*slog.Logger, error) {
	h, err := buildHandler(cfg, w, opts)
	if err != nil {
		return nil, err
	}
	return slog.New(h), nil
}

// buildHandler собирает цепочку: redact → RegisterAttr-атрибуты → fan-out
// (JSON с trace_id/span_id, OTel). Redact снаружи, чтобы покрыть и With()-attrs.
func buildHandler(cfg Config, w io.Writer, opts []Option) (slog.Handler, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	lvl, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	var h slog.Handler = &traceHandler{Handler: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})}
	if o.otel != nil {
		h = newFanout(h, NewOTelHandler(o.otel, lvl))
	}
	h = &ctxAttrsHandler{Handler: h}
	if o.service != nil {
		h = h.WithAttrs(serviceAttrs(*o.service))
	}
	if o.redact.Enabled {
		if h, err = NewRedactHandler(h, o.redact); err != nil {
			return nil, err
		}
	}
	return h, nil
}

func serviceAttrs(cfg ServiceConfig) []slog.Attr {
	var attrs []slog.Attr
	if cfg.Service != "" {
		attrs = append(attrs, slog.String("service", cfg.Service))
	}
	if cfg.Env != "" {
		attrs = append(attrs, slog.String("env", cfg.Env))
	}
	if cfg.Version != "" {
		attrs = append(attrs, slog.String("version", cfg.Version))
	}
	return attrs
}

type slogLogger struct {
	l *slog.Logger
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
