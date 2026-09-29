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

var (
	checks = otelkit.Must(otelkit.Meter("bruteforcekit").Int64Counter(
		"bruteforce_checks_total",
		metric.WithDescription("Guard attempts by result (allowed|locked)"),
	))
	locks = otelkit.Must(otelkit.Meter("bruteforcekit").Int64Counter(
		"bruteforce_locks_total",
		metric.WithDescription("Transitions into the locked state (no active lock -> active lock)"),
	))
	cleanupDeleted = otelkit.Must(otelkit.Meter("bruteforcekit").Int64Counter(
		"bruteforce_cleanup_deleted_total",
		metric.WithDescription("Rows deleted by Cleanup"),
	))
)

func resultAttr(result string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("result", result))
}
