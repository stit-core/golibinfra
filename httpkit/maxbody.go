package httpkit

import (
	"errors"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MaxBodyConfig — потолок тела запроса; MaxBytes = 0 выключает лимит.
type MaxBodyConfig struct {
	// MaxBytes = 0 выключает лимит.
	MaxBytes int64 `envconfig:"HTTP_MAX_BODY_BYTES" default:"1048576"`
}

// MaxBody ограничивает тело через http.MaxBytesReader: handler, прочитавший
// больше лимита, получает *http.MaxBytesError, а ответ 413 засчитывается в метрику.
func MaxBody(cfg MaxBodyConfig) func(http.Handler) http.Handler {
	if cfg.MaxBytes <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	instr := newInstruments()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBytes)
			ww := wrap(w)
			next.ServeHTTP(ww, r)
			if ww.status == http.StatusRequestEntityTooLarge {
				instr.maxBodyRejections.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("path", routeLabel(r)),
				))
			}
		})
	}
}

// IsMaxBodyError — helper для caller'ов, которые сами читают body.
func IsMaxBodyError(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}
