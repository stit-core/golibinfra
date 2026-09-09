package pgkit

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

// RolePrimary — значение label role для пула primary.
const RolePrimary = "primary"

// RoleReplica — значение label role для пула реплики.
const RoleReplica = "replica"

// InstrumentPool вешает pg_pool_* (label role) на пул через глобальный Meter;
// снять регистрацию на shutdown — Registration.Unregister.
func InstrumentPool(pool *pgxpool.Pool, role string) metric.Registration {
	m := otelkit.Meter("pgkit")
	acquired := otelkit.Must(m.Int64ObservableGauge("pg_pool_acquired_conns",
		metric.WithDescription("Connections currently acquired from pool"),
	))
	idle := otelkit.Must(m.Int64ObservableGauge("pg_pool_idle_conns",
		metric.WithDescription("Idle connections in pool"),
	))
	total := otelkit.Must(m.Int64ObservableGauge("pg_pool_total_conns",
		metric.WithDescription("Total open connections in pool"),
	))
	maxc := otelkit.Must(m.Int64ObservableGauge("pg_pool_max_conns",
		metric.WithDescription("Configured pool maximum"),
	))
	constructing := otelkit.Must(m.Int64ObservableGauge("pg_pool_constructing_conns",
		metric.WithDescription("Connections currently being constructed"),
	))
	emptyAcq := otelkit.Must(m.Int64ObservableCounter("pg_pool_empty_acquire_total",
		metric.WithDescription("Acquires that had to wait because pool was empty"),
	))
	canceledAcq := otelkit.Must(m.Int64ObservableCounter("pg_pool_canceled_acquire_total",
		metric.WithDescription("Acquires canceled by ctx before completion"),
	))
	acqTotal := otelkit.Must(m.Int64ObservableCounter("pg_pool_acquire_total",
		metric.WithDescription("Total successful acquire calls"),
	))
	acqDur := otelkit.Must(m.Float64ObservableCounter("pg_pool_acquire_duration_seconds_total",
		metric.WithDescription("Cumulative time spent waiting for acquire"),
		metric.WithUnit("s"),
	))

	attrs := metric.WithAttributes(attribute.String("role", role))
	return otelkit.Must(m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := pool.Stat()
		o.ObserveInt64(acquired, int64(s.AcquiredConns()), attrs)
		o.ObserveInt64(idle, int64(s.IdleConns()), attrs)
		o.ObserveInt64(total, int64(s.TotalConns()), attrs)
		o.ObserveInt64(maxc, int64(s.MaxConns()), attrs)
		o.ObserveInt64(constructing, int64(s.ConstructingConns()), attrs)
		o.ObserveInt64(emptyAcq, s.EmptyAcquireCount(), attrs)
		o.ObserveInt64(canceledAcq, s.CanceledAcquireCount(), attrs)
		o.ObserveInt64(acqTotal, s.AcquireCount(), attrs)
		o.ObserveFloat64(acqDur, s.AcquireDuration().Seconds(), attrs)
		return nil
	},
		acquired, idle, total, maxc, constructing,
		emptyAcq, canceledAcq, acqTotal, acqDur,
	))
}
