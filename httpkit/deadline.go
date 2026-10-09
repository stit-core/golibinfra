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

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// codeRequestTimeout — code 503, которым deadline отвечает за хендлер, не начавший ответ к дедлайну.
const codeRequestTimeout = "REQUEST_TIMEOUT"

// deadline ставит ctx запроса дедлайн timeout (HTTP_REQUEST_TIMEOUT). Ответ, не начатый к дедлайну,
// — 503 problem+json сразу, до WriteTimeout, иначе клиент получил бы обрыв; запись хендлера после
// этого молча отбрасывается: huma паникует на ошибке записи, а ответ хендлера ещё нужен
// Idempotency. Middleware выше ждут возврата хендлера: резерв идемпотентности и учёт запросов в
// полёте его переживают. Запрос потока (streaming) идёт дальше без дедлайна, с отметкой: поток
// определяет маршрут, а не заголовок клиента, и операции без потока дедлайн ставит её хендлер
// (operationDeadline).
func deadline(timeout time.Duration, log logkit.Logger) func(http.Handler) http.Handler {
	if timeout <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if streaming(r) {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), streamClaimKey{}, true)))
				return
			}
			serveWithDeadline(timeout, log, next, w, r)
		})
	}
}

func serveWithDeadline(timeout time.Duration, log logkit.Logger, next http.Handler, w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	dw := &deadlineWriter{w: w, h: w.Header().Clone(), ctx: ctx, timeout: timeout, log: log}
	timer := time.AfterFunc(timeout, dw.expire)
	defer func() {
		timer.Stop()
		dw.finish()
	}()
	next.ServeHTTP(dw, r.WithContext(ctx))
}

// streamClaimKey — отметка ctx: deadline пропустил запрос как поток по заголовку клиента.
type streamClaimKey struct{}

// streaming — запрос называет себя потоком: GET с SSE или upgrade (WebSocket); дедлайн оборвал бы
// поток. Заголовок шлёт клиент, поэтому запись (POST, PUT, PATCH, DELETE) им дедлайна не снимает,
// а у GET-операции huma решает её описание (operationDeadline).
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

// operationDeadline — хендлер операции huma под дедлайном, который deadline отложил из-за
// заголовка клиента: поток — только операция с ответом text/event-stream (так её объявляет
// sse.Register), у остальных `Accept: text/event-stream` и `Upgrade` дедлайна не снимают.
func operationDeadline(timeout time.Duration, log logkit.Logger, op *huma.Operation, serve http.HandlerFunc) http.HandlerFunc {
	if timeout <= 0 || eventStream(op) {
		return serve
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if claimed, _ := r.Context().Value(streamClaimKey{}).(bool); claimed {
			serveWithDeadline(timeout, log, serve, w, r)
			return
		}
		serve(w, r)
	}
}

func eventStream(op *huma.Operation) bool {
	for _, resp := range op.Responses {
		if resp != nil && resp.Content["text/event-stream"] != nil {
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
