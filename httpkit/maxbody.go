package httpkit

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MaxBodyConfig — потолок тела запроса; MaxBytes = 0 выключает лимит.
type MaxBodyConfig struct {
	// MaxBytes = 0 выключает лимит.
	MaxBytes int64 `envconfig:"HTTP_MAX_BODY_BYTES" default:"1048576"`
}

// MaxBody ограничивает тело через http.MaxBytesReader: handler, прочитавший больше лимита,
// получает *http.MaxBytesError, ответ — 413, и он засчитывается в метрику. Лимит фиксируется на
// первом чтении тела: операция huma до него ставит свой MaxBodyBytes (хук кита — этот же лимит,
// хук потребителя может его поднять).
func MaxBody(cfg MaxBodyConfig) func(http.Handler) http.Handler {
	if cfg.MaxBytes <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	instr := newInstruments()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := &limitedBody{w: w, body: r.Body, limit: cfg.MaxBytes}
			r.Body = body
			ww := wrap(w)
			next.ServeHTTP(ww, r.WithContext(context.WithValue(r.Context(), bodyLimitKey{}, body)))
			if ww.status == http.StatusRequestEntityTooLarge {
				instr.maxBodyRejections.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("path", routeLabel(r)),
				))
			}
		})
	}
}

// codeBodyTooLarge — code 413 тела сверх лимита: его же объявляет в OpenAPI операция с телом под лимитом.
const codeBodyTooLarge = "BODY_TOO_LARGE"

type bodyLimitKey struct{}

// limitedBody — тело запроса под http.MaxBytesReader с лимитом, который фиксируется на первом
// чтении.
type limitedBody struct {
	w     http.ResponseWriter
	body  io.ReadCloser
	limit int64
	r     io.Reader
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.r == nil {
		b.r = b.body
		if b.limit > 0 {
			b.r = http.MaxBytesReader(b.w, b.body, b.limit)
		}
	}
	return b.r.Read(p)
}

func (b *limitedBody) Close() error { return b.body.Close() }

// operationBodyLimit — huma-middleware: тело операции ограничено её MaxBodyBytes, а не общим
// лимитом MaxBody; без MaxBody в цепочке тело режет свой MaxBytesReader. Не больше нуля — без
// предела, как у huma.
func operationBodyLimit(ctx huma.Context, next func(huma.Context)) {
	limit := ctx.Operation().MaxBodyBytes
	if b, ok := ctx.Context().Value(bodyLimitKey{}).(*limitedBody); ok {
		if b.r == nil {
			b.limit = limit
		}
	} else if r, w := unwrapHTTP(ctx); limit > 0 && r != nil && r.Body != nil {
		// Лимит хука операции группы huma не видит: тело своей копии она читает по литералу.
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
	next(ctx)
}

// stricterLimit — лимит тела, который режет раньше; не больше нуля — без предела.
func stricterLimit(a, b int64) int64 {
	switch {
	case b <= 0:
		return a
	case a <= 0 || b < a:
		return b
	}
	return a
}

// IsMaxBodyError — helper для caller'ов, которые сами читают body.
func IsMaxBodyError(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}
