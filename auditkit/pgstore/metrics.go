package pgstore

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

// Потребители обычно логируют ошибку Log и продолжают доменную операцию —
// счётчик по result единственный алертируемый сигнал потери записи аудита.
const (
	resultOK      = "ok"
	resultError   = "error"
	resultInvalid = "invalid"
)

type instruments struct {
	writes        metric.Int64Counter
	writeDuration metric.Float64Histogram
}

// newInstruments зовётся в NewStore: инструмент берёт текущий глобальный
// провайдер, а не первый провайдер процесса.
func newInstruments() *instruments {
	m := otelkit.Meter("auditkit")
	return &instruments{
		writes: otelkit.Must(m.Int64Counter("audit_log_writes_total",
			metric.WithDescription("Audit log writes by result (ok|error|invalid)"),
		)),
		writeDuration: otelkit.Must(m.Float64Histogram("audit_log_write_duration_seconds",
			metric.WithDescription("Audit log INSERT duration"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
	}
}

func resultAttr(result string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("result", result))
}

func (i *instruments) invalid(ctx context.Context) {
	i.writes.Add(ctx, 1, resultAttr(resultInvalid))
}

func (i *instruments) observeWrite(ctx context.Context, start time.Time, err error) {
	result := resultOK
	if err != nil {
		result = resultError
	}
	i.writes.Add(ctx, 1, resultAttr(result))
	i.writeDuration.Record(ctx, time.Since(start).Seconds(), resultAttr(result))
}
