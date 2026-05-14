package pgkit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
)

// InstrumentPool регистрирует ObservableGauge'и поверх pgxpool.Stat() и
// counter'ы для cumulative-counters (AcquireCount, CanceledAcquireCount,
// AcquireDuration). Возвращаемый Registration позволяет отписаться при
// graceful shutdown; обычно вызывается из fx-invoker и привязывается к
// lifecycle.
//
// Эмитируемые метрики:
//
//	db_pool_acquired_conns         (gauge)    — занятые соединения
//	db_pool_idle_conns             (gauge)    — простаивающие
//	db_pool_total_conns            (gauge)    — всего открытых
//	db_pool_max_conns              (gauge)    — лимит
//	db_pool_constructing_conns     (gauge)    — устанавливаемые соединения
//	db_pool_empty_acquire_total    (counter)  — acquire'ы из пустого пула
//	db_pool_canceled_acquire_total (counter)  — отменённых acquire
//	db_pool_acquire_total          (counter)  — всего acquire'ов
//	db_pool_acquire_duration_seconds_total (counter) — суммарное время ожидания
//
// Все метрики attribute-less per-pool. Если в приложении несколько пулов —
// различай их через resource attributes или регистрируй каждый под
// собственным OTel scope.
func InstrumentPool(pool *pgxpool.Pool, meter metric.Meter) (metric.Registration, error) {
	acquired, err := meter.Int64ObservableGauge("db_pool_acquired_conns",
		metric.WithDescription("Connections currently acquired from pool"),
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: gauge acquired: %w", err)
	}
	idle, err := meter.Int64ObservableGauge("db_pool_idle_conns",
		metric.WithDescription("Idle connections in pool"),
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: gauge idle: %w", err)
	}
	total, err := meter.Int64ObservableGauge("db_pool_total_conns",
		metric.WithDescription("Total open connections in pool"),
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: gauge total: %w", err)
	}
	maxc, err := meter.Int64ObservableGauge("db_pool_max_conns",
		metric.WithDescription("Configured pool maximum"),
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: gauge max: %w", err)
	}
	constructing, err := meter.Int64ObservableGauge("db_pool_constructing_conns",
		metric.WithDescription("Connections currently being constructed"),
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: gauge constructing: %w", err)
	}
	emptyAcq, err := meter.Int64ObservableCounter("db_pool_empty_acquire_total",
		metric.WithDescription("Acquires that had to wait because pool was empty"),
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: counter empty_acquire: %w", err)
	}
	canceledAcq, err := meter.Int64ObservableCounter("db_pool_canceled_acquire_total",
		metric.WithDescription("Acquires canceled by ctx before completion"),
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: counter canceled_acquire: %w", err)
	}
	acqTotal, err := meter.Int64ObservableCounter("db_pool_acquire_total",
		metric.WithDescription("Total successful acquire calls"),
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: counter acquire_total: %w", err)
	}
	acqDur, err := meter.Float64ObservableCounter("db_pool_acquire_duration_seconds_total",
		metric.WithDescription("Cumulative time spent waiting for acquire"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: counter acquire_duration: %w", err)
	}

	reg, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := pool.Stat()
		o.ObserveInt64(acquired, int64(s.AcquiredConns()))
		o.ObserveInt64(idle, int64(s.IdleConns()))
		o.ObserveInt64(total, int64(s.TotalConns()))
		o.ObserveInt64(maxc, int64(s.MaxConns()))
		o.ObserveInt64(constructing, int64(s.ConstructingConns()))
		o.ObserveInt64(emptyAcq, s.EmptyAcquireCount())
		o.ObserveInt64(canceledAcq, s.CanceledAcquireCount())
		o.ObserveInt64(acqTotal, s.AcquireCount())
		o.ObserveFloat64(acqDur, s.AcquireDuration().Seconds())
		return nil
	},
		acquired, idle, total, maxc, constructing,
		emptyAcq, canceledAcq, acqTotal, acqDur,
	)
	if err != nil {
		return nil, fmt.Errorf("pgkit: register callback: %w", err)
	}
	return reg, nil
}
