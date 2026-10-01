package pgstore

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

const (
	resultAllowed = "allowed"
	resultLocked  = "locked"
)

type instruments struct {
	checks         metric.Int64Counter
	locks          metric.Int64Counter
	cleanupDeleted metric.Int64Counter
}

// newInstruments зовётся в NewStore: инструмент берёт текущий глобальный
// провайдер, а не первый провайдер процесса.
func newInstruments() *instruments {
	m := otelkit.Meter("bruteforcekit")
	return &instruments{
		checks: otelkit.Must(m.Int64Counter("bruteforce_checks_total",
			metric.WithDescription("Guard attempts by result (allowed|locked)"),
		)),
		locks: otelkit.Must(m.Int64Counter("bruteforce_locks_total",
			metric.WithDescription("Transitions into the locked state (no active lock -> active lock)"),
		)),
		cleanupDeleted: otelkit.Must(m.Int64Counter("bruteforce_cleanup_deleted_total",
			metric.WithDescription("Rows deleted by Cleanup"),
		)),
	}
}

func resultAttr(result string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("result", result))
}
