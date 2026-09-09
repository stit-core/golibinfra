// Package pgtest — фейки pgkit.Transactor для тестов домена без Postgres.
package pgtest

import (
	"context"
	"sync/atomic"

	"github.com/jackc/pgx/v5"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

var (
	_ pgkit.Transactor = Identity{}
	_ pgkit.Transactor = (*Recorder)(nil)
)

// Identity — pgkit.Transactor, прогоняющий fn без транзакции.
type Identity struct{}

// WithTx вызывает fn с ctx, помеченным pgkit.WithTestTx.
func (Identity) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(pgkit.WithTestTx(ctx))
}

// WithTxOptions вызывает fn с ctx, помеченным pgkit.WithTestTx; опции игнорируются.
func (Identity) WithTxOptions(ctx context.Context, _ pgx.TxOptions, fn func(ctx context.Context) error) error {
	return fn(pgkit.WithTestTx(ctx))
}

// Recorder — Identity со счётчиком вызовов.
type Recorder struct {
	calls atomic.Int64
}

// WithTx считает вызов и прогоняет fn с ctx, помеченным pgkit.WithTestTx.
func (r *Recorder) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	r.calls.Add(1)
	return fn(pgkit.WithTestTx(ctx))
}

// WithTxOptions считает вызов и прогоняет fn с ctx, помеченным pgkit.WithTestTx.
func (r *Recorder) WithTxOptions(ctx context.Context, _ pgx.TxOptions, fn func(ctx context.Context) error) error {
	r.calls.Add(1)
	return fn(pgkit.WithTestTx(ctx))
}

// Calls — число вызовов WithTx и WithTxOptions.
func (r *Recorder) Calls() int { return int(r.calls.Load()) }
