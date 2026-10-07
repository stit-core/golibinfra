// Package pgtest — фейки pgkit.Transactor для тестов домена без Postgres: транзакция фейка
// откатывает записи фейков testkit (outboxtest, audittest), если fn вернул ошибку.
package pgtest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

var (
	_ pgkit.Transactor = Identity{}
	_ pgkit.Transactor = (*Recorder)(nil)
)

// Identity — pgkit.Transactor без Postgres: fn идёт в транзакции фейка (pgkit.InTx и InTx —
// true); ошибка или panic fn откатывает записи фейков, отменённый ctx — ошибка до fn, вложенный
// вызов идёт во внешней транзакции.
type Identity struct{}

// WithTx прогоняет fn в транзакции фейка.
func (Identity) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return run(ctx, fn)
}

// WithTxOptions прогоняет fn в транзакции фейка; опции игнорируются.
func (Identity) WithTxOptions(ctx context.Context, _ pgx.TxOptions, fn func(ctx context.Context) error) error {
	return run(ctx, fn)
}

// Recorder — Identity со счётчиком вызовов.
type Recorder struct {
	calls atomic.Int64
}

// WithTx считает вызов и прогоняет fn в транзакции фейка.
func (r *Recorder) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	r.calls.Add(1)
	return run(ctx, fn)
}

// WithTxOptions считает вызов и прогоняет fn в транзакции фейка; опции игнорируются.
func (r *Recorder) WithTxOptions(ctx context.Context, _ pgx.TxOptions, fn func(ctx context.Context) error) error {
	r.calls.Add(1)
	return run(ctx, fn)
}

// Calls — число вызовов WithTx и WithTxOptions.
func (r *Recorder) Calls() int { return int(r.calls.Load()) }

// InTx — ctx несёт транзакцию фейка (Identity, Recorder), а не только метку pgkit.WithTestTx:
// так фейки testkit отличают транзакцию кода под тестом от метки, поставленной самим тестом.
func InTx(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{}).(*tx)
	return ok
}

// OnRollback регистрирует отмену записи фейка в транзакции ctx: её зовёт откат (ошибка или panic
// fn). false — в ctx нет транзакции фейка, отменять нечего.
func OnRollback(ctx context.Context, undo func()) bool {
	t, ok := ctx.Value(txKey{}).(*tx)
	if !ok {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.undo = append(t.undo, undo)
	return true
}

type txKey struct{}

type tx struct {
	mu   sync.Mutex
	undo []func()
}

func run(ctx context.Context, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("pgtest: begin tx: %w", err)
	}
	t := &tx{}
	committed := false
	defer func() {
		if !committed {
			t.rollback()
		}
	}()
	if err := fn(context.WithValue(pgkit.WithTestTx(ctx), txKey{}, t)); err != nil {
		return err
	}
	committed = true
	return nil
}

// rollback — в обратном порядке, как откат отменяет записи транзакции.
func (t *tx) rollback() {
	t.mu.Lock()
	undo := t.undo
	t.undo = nil
	t.mu.Unlock()
	for _, u := range slices.Backward(undo) {
		u()
	}
}
