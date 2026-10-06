package httpkit

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// codeRequestTimeout — code 503, которым deadline отвечает за хендлер, не начавший ответ к дедлайну.
const codeRequestTimeout = "REQUEST_TIMEOUT"

// deadline ставит ctx запроса дедлайн timeout (HTTP_REQUEST_TIMEOUT). Ответ, не начатый к дедлайну,
// — 503 problem+json сразу, до WriteTimeout, иначе клиент получил бы обрыв; запись хендлера после
// этого отбрасывается (http.ErrHandlerTimeout). Middleware выше ждут возврата хендлера: резерв
// идемпотентности и учёт запросов в полёте его переживают. Потоки — SSE (Accept:
// text/event-stream) и upgrade — идут без дедлайна.
func deadline(timeout time.Duration, log logkit.Logger) func(http.Handler) http.Handler {
	if timeout <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if streaming(r) {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			dw := &deadlineWriter{w: w, h: w.Header().Clone()}
			timer := time.AfterFunc(timeout, func() {
				if dw.expire(ctx) {
					log.Warn(ctx, "request deadline exceeded", "timeout", timeout.String())
				}
			})
			defer func() {
				timer.Stop()
				dw.finish()
			}()
			next.ServeHTTP(dw, r.WithContext(ctx))
		})
	}
}

// streaming — запрос потока: SSE или upgrade (WebSocket); дедлайн оборвал бы его.
func streaming(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	for _, accept := range r.Header.Values("Accept") {
		if strings.Contains(strings.ToLower(accept), "text/event-stream") {
			return true
		}
	}
	return false
}

// deadlineWriter — writer хендлера под deadline: заголовки копятся в своей карте до WriteHeader,
// запись хендлера и 503 дедлайна (из горутины таймера) идут под одним мьютексом.
type deadlineWriter struct {
	mu          sync.Mutex
	w           http.ResponseWriter
	h           http.Header
	wroteHeader bool
	timedOut    bool
	done        bool
}

func (d *deadlineWriter) Header() http.Header { return d.h }

func (d *deadlineWriter) WriteHeader(code int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writeHeader(code)
}

// writeHeader переносит заголовки хендлера в ответ и пишет статус; вызывается под d.mu.
func (d *deadlineWriter) writeHeader(code int) {
	if d.wroteHeader || d.timedOut {
		return
	}
	d.wroteHeader = true
	dst := d.w.Header()
	for k := range dst {
		if _, ok := d.h[k]; !ok {
			delete(dst, k)
		}
	}
	maps.Copy(dst, d.h)
	d.w.WriteHeader(code)
}

func (d *deadlineWriter) Write(b []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timedOut {
		return 0, http.ErrHandlerTimeout
	}
	d.writeHeader(http.StatusOK)
	return d.w.Write(b)
}

func (d *deadlineWriter) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timedOut {
		return
	}
	d.writeHeader(http.StatusOK)
	if f, ok := d.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (d *deadlineWriter) Unwrap() http.ResponseWriter { return d.w }

// expire — дедлайн: ответ не начат — 503 problem+json и Flush, чтобы клиент получил его до
// WriteTimeout; true, если 503 записан.
func (d *deadlineWriter) expire(ctx context.Context) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.wroteHeader || d.done {
		return false
	}
	d.timedOut = true
	writeAPIError(ctx, d.w, Unavailable(codeRequestTimeout, ""))
	if f, ok := d.w.(http.Flusher); ok {
		f.Flush()
	}
	return true
}

// finish — хендлер вернулся: таймер больше не пишет в ответ.
func (d *deadlineWriter) finish() {
	d.mu.Lock()
	d.done = true
	d.mu.Unlock()
}
