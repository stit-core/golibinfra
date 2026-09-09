package logkit

import (
	"context"
	"io"
	"log/slog"
	"os"
	"runtime"
	"time"

	"go.opentelemetry.io/otel/log"
)

// Option настраивает сборку логгера; порядок применения — порядок аргументов.
type Option func(*options)

type options struct {
	redact  RedactConfig
	otel    log.LoggerProvider
	service *ServiceConfig
	writer  io.Writer
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

// WithoutOTel убирает OTel-sink, заданный предыдущими опциями: логгер пишет
// только в локальный sink.
func WithoutOTel() Option {
	return func(o *options) { o.otel = nil }
}

// WithService добавляет статические поля service/env/version в каждую запись.
func WithService(cfg ServiceConfig) Option {
	return func(o *options) { o.service = &cfg }
}

// WithWriter отправляет локальный sink в w вместо stdout; приоритетнее
// Config.Silent.
func WithWriter(w io.Writer) Option {
	return func(o *options) { o.writer = w }
}

// New собирает Logger по cfg и опциям.
func New(cfg Config, opts ...Option) (Logger, error) {
	l, err := NewSlog(cfg, opts...)
	if err != nil {
		return nil, err
	}
	return FromSlog(l), nil
}

// NewSlog собирает *slog.Logger с той же цепочкой handler'ов, что и New — для
// библиотек, принимающих *slog.Logger.
func NewSlog(cfg Config, opts ...Option) (*slog.Logger, error) {
	h, err := buildHandler(cfg, opts)
	if err != nil {
		return nil, err
	}
	return slog.New(h), nil
}

// FromSlog оборачивает готовый *slog.Logger: обе формы логгера делят одну
// цепочку handler'ов и одну метрику. nil даёт Nop().
func FromSlog(l *slog.Logger) Logger {
	if l == nil {
		return Nop()
	}
	return &slogLogger{l: l}
}

// Redact — снаружи цепочки, чтобы покрыть и With()-attrs; метрика — под ним,
// чтобы считать эмитированные записи, а не вызовы.
func buildHandler(cfg Config, opts []Option) (slog.Handler, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	lvl, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	format, err := parseFormat(cfg.Format)
	if err != nil {
		return nil, err
	}

	w := o.writer
	if w == nil {
		w = os.Stdout
		if cfg.Silent {
			w = io.Discard
		}
	}
	ho := &slog.HandlerOptions{Level: lvl, AddSource: cfg.Source}
	var local slog.Handler = slog.NewJSONHandler(w, ho)
	if format == formatText {
		local = slog.NewTextHandler(w, ho)
	}

	var service []slog.Attr
	if o.service != nil {
		service = serviceAttrs(*o.service)
	}
	if len(service) > 0 {
		local = local.WithAttrs(service)
	}

	var h slog.Handler = newCtxHandler(local, traceCtxAttrs)
	if o.otel != nil {
		otelSink := NewOTelHandler(o.otel, lvl)
		if len(service) > 0 {
			otelSink = otelSink.WithAttrs(service)
		}
		h = newFanout(h, otelSink)
	}
	h = newCtxHandler(h, registeredCtxAttrs)
	h = &metricHandler{Handler: h}
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
	s.log(ctx, slog.LevelDebug, msg, kv)
}

func (s *slogLogger) Info(ctx context.Context, msg string, kv ...any) {
	s.log(ctx, slog.LevelInfo, msg, kv)
}

func (s *slogLogger) Warn(ctx context.Context, msg string, kv ...any) {
	s.log(ctx, slog.LevelWarn, msg, kv)
}

func (s *slogLogger) Error(ctx context.Context, msg string, kv ...any) {
	s.log(ctx, slog.LevelError, msg, kv)
}

func (s *slogLogger) With(kv ...any) Logger {
	return &slogLogger{l: s.l.With(kv...)}
}

func (s *slogLogger) Enabled(ctx context.Context, level slog.Level) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.l.Enabled(ctx, level)
}

// log снимает PC вызывающего сам: с LOG_SOURCE поле source должно указывать на
// код сервиса, а не на обёртку logkit.
func (s *slogLogger) log(ctx context.Context, level slog.Level, msg string, kv []any) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !s.l.Enabled(ctx, level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(3, pcs[:])
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(kv...)
	_ = s.l.Handler().Handle(ctx, r)
}
