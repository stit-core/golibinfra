package httpkit

import (
	"errors"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type MaxBodyConfig struct {
	MaxBytes int64 `envconfig:"HTTP_MAX_BODY_BYTES" default:"1048576"`
}

// maxBodyResponseWriter перехватывает 413-ответ от MaxBytesReader. Если
// handler попытается прочитать body длиннее лимита, MaxBytesReader
// возвращает *http.MaxBytesError и автоматически устанавливает
// статус 413; нам нужно его засчитать.
type maxBodyResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *maxBodyResponseWriter) WriteHeader(s int) {
	w.status = s
	w.ResponseWriter.WriteHeader(s)
}

func MaxBody(cfg MaxBodyConfig) func(http.Handler) http.Handler {
	if cfg.MaxBytes == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	instr := newHTTPInstruments()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBytes)
			rec := &maxBodyResponseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			// MaxBytesReader устанавливает 413 → засчитываем как rejection.
			if rec.status == http.StatusRequestEntityTooLarge {
				instr.maxBodyRejections.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("path", r.URL.Path),
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
