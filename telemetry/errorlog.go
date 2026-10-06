package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
)

// errorLogWindow — как часто повторяющееся сообщение доходит до лога.
// Недоступный коллектор ошибается на каждом экспорте трёх сигналов.
const errorLogWindow = time.Minute

// errorLogKeys — потолок отслеживаемых текстов: ошибки экспорта несут адреса и
// коды, множество текстов не ограничено.
const errorLogKeys = 256

// Option настраивает Init.
type Option func(*options)

type options struct {
	errorLog slog.Handler
}

// WithErrorLogger направляет ошибки экспорта и внутренние сообщения OTel SDK в
// этот slog.Handler вместо stderr. Handler не должен иметь OTel-sink'а: ошибка
// экспорта логов породила бы новую запись, новый экспорт и новую ошибку.
func WithErrorLogger(h slog.Handler) Option {
	return func(o *options) { o.errorLog = h }
}

// installErrorLogging уводит два независимых канала диагностики SDK в один
// handler: otel.SetErrorHandler (ошибки экспорта, уровень error) и
// otel.SetLogger (внутренние сообщения SDK: Warn → warn, Info/Debug → debug).
func installErrorLogging(h slog.Handler) {
	if h == nil {
		return
	}
	lim := newLimiter(errorLogWindow, time.Now)
	// Ошибки экспорта дросселируются по своему тексту, а не по общему сообщению
	// записи, — поэтому handler для них берётся без limitHandler'а.
	log := slog.New(h)

	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		if err == nil || !lim.allow(err.Error()) {
			return
		}
		log.LogAttrs(context.Background(), slog.LevelError, "telemetry: otel sdk error", slog.String("err", err.Error()))
	}))
	otel.SetLogger(logr.FromSlogHandler(&logrHandler{Handler: &limitHandler{Handler: h, lim: lim}}))
}

// logrHandler переводит verbosity logr в slog-шкалу. logr кодирует V(n) как
// уровень -n (go-logr/logr/slogsink.go), а SDK шлёт global.Warn через V(1) и
// global.Info/Debug через V(4)/V(8): без перевода на пороге info в лог не
// попадает ничего, кроме global.Error, — включая «dropped log records».
type logrHandler struct{ slog.Handler }

func (h *logrHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.Handler.Enabled(ctx, sdkLevel(l))
}

func (h *logrHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Level = sdkLevel(r.Level)
	return h.Handler.Handle(ctx, r)
}

func (h *logrHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logrHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *logrHandler) WithGroup(name string) slog.Handler {
	return &logrHandler{Handler: h.Handler.WithGroup(name)}
}

// sdkVerbosityWarn — самая низкая V-verbosity, которую SDK считает
// предупреждением: V(1) у global.Warn против V(4) у global.Info.
const sdkVerbosityWarn = slog.LevelInfo - 3

func sdkLevel(l slog.Level) slog.Level {
	switch {
	case l >= slog.LevelInfo:
		return l
	case l >= sdkVerbosityWarn:
		return slog.LevelWarn
	default:
		return slog.LevelDebug
	}
}

// limiter пропускает ключ не чаще раза в window.
type limiter struct {
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time
}

func newLimiter(window time.Duration, now func() time.Time) *limiter {
	return &limiter{window: window, now: now, seen: map[string]time.Time{}}
}

func (l *limiter) allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if last, ok := l.seen[key]; ok && now.Sub(last) < l.window {
		return false
	}
	if len(l.seen) >= errorLogKeys {
		for k, t := range l.seen {
			if now.Sub(t) >= l.window {
				delete(l.seen, k)
			}
		}
		if len(l.seen) >= errorLogKeys {
			clear(l.seen)
		}
	}
	l.seen[key] = now
	return true
}

// limitHandler — slog.Handler, который дросселирует записи по тексту сообщения.
// Сообщение об одном и том же недоступном коллекторе печатается раз в окно, а
// не на каждый экспорт.
type limitHandler struct {
	slog.Handler
	lim *limiter
}

func (h *limitHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.lim.allow(r.Message) {
		return nil
	}
	return h.Handler.Handle(ctx, r)
}

func (h *limitHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &limitHandler{Handler: h.Handler.WithAttrs(attrs), lim: h.lim}
}

func (h *limitHandler) WithGroup(name string) slog.Handler {
	return &limitHandler{Handler: h.Handler.WithGroup(name), lim: h.lim}
}
