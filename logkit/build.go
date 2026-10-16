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
	redact         RedactConfig
	otel           log.LoggerProvider
	service        *ServiceConfig
	writer         io.Writer
	stderrFallback bool
}

// WithRedact настраивает маскирование: свои маски, замена или выключение
// (Disabled). Без опции логгер маскирует дефолтным набором.
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

// WithStderrFallback оставляет канал логгеру без OTel-sink при Config.Silent: записи уровня ERROR
// идут в stderr, а не отбрасываются. Для логгеров без OTel-sink по построению — событий fx
// (FxLogger), команды (NewFromEnv) и ошибок экспорта telemetry: Silent рассчитан на OTel-sink.
func WithStderrFallback() Option {
	return func(o *options) { o.stderrFallback = true }
}

// New собирает Logger по cfg и опциям.
func New(cfg Config, opts ...Option) (Logger, error) {
	l, err := NewSlog(cfg, opts...)
	if err != nil {
		return nil, err
	}
	return FromSlog(l), nil
}

// NewFromEnv собирает Logger команды вне fx (миграции, разовые задачи) тем же разбором окружения,
// что граф с Module: LOG_* — NewConfig, LOG_REDACT_* — NewRedactConfig. OTel-sink'а у логгера нет,
// поэтому при LOG_SILENT записи ERROR идут в stderr (WithStderrFallback). Негодное значение — ErrConfig.
func NewFromEnv() (Logger, error) {
	cfg, err := NewConfig()
	if err != nil {
		return nil, err
	}
	redact, err := NewRedactConfig()
	if err != nil {
		return nil, err
	}
	return New(cfg, WithRedact(redact), WithStderrFallback())
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

	w, localLvl := o.writer, lvl
	if w == nil {
		w = os.Stdout
		switch {
		case cfg.Silent && o.stderrFallback:
			w, localLvl = os.Stderr, max(lvl, slog.LevelError)
		case cfg.Silent:
			w = io.Discard
		}
	}
	ho := &slog.HandlerOptions{Level: localLvl, AddSource: cfg.Source}
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
	h = &metricHandler{Handler: h, messages: newMessagesCounter()}
	if !o.redact.Disabled {
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

// MaskText — text по правилам масок этого логгера: киты пропускают через них текст ошибки
// потребителя перед записью в спан (internal/spanerr), чтобы скрытое в логе не лежало открытым
// в трассе. Логгер с выключенным redact и логгер поверх чужого slog.Handler текст не меняют —
// как и запись лога.
func (s *slogLogger) MaskText(text string) string {
	if h, ok := s.l.Handler().(*redactHandler); ok {
		return h.text(text)
	}
	return text
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
