package httpkit

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// codeRequestTimeout — code 503, которым deadline отвечает за хендлер, не начавший ответ к дедлайну.
const codeRequestTimeout = "REQUEST_TIMEOUT"

// deadline ставит ctx запроса дедлайн timeout (HTTP_REQUEST_TIMEOUT). Ответ, не начатый к дедлайну,
// — 503 problem+json сразу, до WriteTimeout, иначе клиент получил бы обрыв; запись хендлера после
// этого молча отбрасывается: huma паникует на ошибке записи, а ответ хендлера ещё нужен
// Idempotency. Middleware выше ждут возврата хендлера: резерв идемпотентности и учёт запросов в
// полёте его переживают. Потоки — GET с Accept: text/event-stream (SSE) или Upgrade — идут без
// дедлайна.
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
			dw := &deadlineWriter{w: w, h: w.Header().Clone(), ctx: ctx, timeout: timeout, log: log}
			timer := time.AfterFunc(timeout, dw.expire)
			defer func() {
				timer.Stop()
				dw.finish()
			}()
			next.ServeHTTP(dw, r.WithContext(ctx))
		})
	}
}

// streaming — запрос потока: GET с SSE или upgrade (WebSocket); дедлайн оборвал бы его. Заголовок
// шлёт клиент, поэтому запись (POST, PUT, PATCH, DELETE) им дедлайна не снимает.
func streaming(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
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
	ctx         context.Context
	timeout     time.Duration
	log         logkit.Logger
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

// writeHeader переносит заголовки хендлера в ответ и пишет статус, а после дедлайна — 503 вместо
// них: хендлер, ответивший на отмену ctx, не обгоняет таймер. Вызывается под d.mu.
func (d *deadlineWriter) writeHeader(code int) {
	if d.wroteHeader || d.timedOut {
		return
	}
	if errors.Is(d.ctx.Err(), context.DeadlineExceeded) {
		d.timeOut()
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
	d.writeHeader(http.StatusOK)
	if d.timedOut {
		return len(b), nil
	}
	return d.w.Write(b)
}

func (d *deadlineWriter) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writeHeader(http.StatusOK)
	if d.timedOut {
		return
	}
	if f, ok := d.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (d *deadlineWriter) Unwrap() http.ResponseWriter { return d.w }

// expire — таймер дедлайна: ответ не начат и хендлер ещё работает — 503.
func (d *deadlineWriter) expire() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.wroteHeader && !d.timedOut && !d.done {
		d.timeOut()
	}
}

// finish — хендлер вернулся: таймер больше не пишет в ответ, а хендлер, вернувшийся после дедлайна
// без ответа, получает 503, как и не дождавшийся таймера.
func (d *deadlineWriter) finish() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.wroteHeader && !d.timedOut && errors.Is(d.ctx.Err(), context.DeadlineExceeded) {
		d.timeOut()
	}
	d.done = true
}

// timeOut пишет 503 REQUEST_TIMEOUT целым ответом (Content-Length) с Flush: клиент дочитывает его,
// не дожидаясь хендлера, а по Connection: close не шлёт следующий запрос в соединение, которое
// хендлер держит до возврата. Вызывается под d.mu.
func (d *deadlineWriter) timeOut() {
	d.timedOut = true
	body, _ := json.Marshal(Unavailable(codeRequestTimeout, "").forRequest(d.ctx))
	h := d.w.Header()
	h.Set("Content-Type", ProblemContentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Connection", "close")
	d.w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = d.w.Write(body)
	if f, ok := d.w.(http.Flusher); ok {
		f.Flush()
	}
	d.log.Warn(d.ctx, "request deadline exceeded", "timeout", d.timeout.String())
}
