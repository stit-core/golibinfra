package pgkit

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Transactor interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error
}

type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type PoolTransactor struct {
	pool *pgxpool.Pool
}

func NewPoolTransactor(pool *pgxpool.Pool) *PoolTransactor {
	return &PoolTransactor{pool: pool}
}

type txCtxKey struct{}

func Conn(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if tx, ok := ctx.Value(txCtxKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}

// InTx сообщает, несёт ли ctx активную транзакцию (открытую Transactor.WithTx).
func InTx(ctx context.Context) bool {
	_, ok := ctx.Value(txCtxKey{}).(pgx.Tx)
	return ok
}

func (t *PoolTransactor) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return t.WithTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, fn)
}

// WithTxOptions открывает транзакцию с заданными опциями; вложенный вызов
// участвует во внешней транзакции, и её опции имеют приоритет.
func (t *PoolTransactor) WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}

	tx, err := t.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("pgkit: begin tx: %w", err)
	}
	// Откат в defer спасает соединение при panic внутри fn; после Commit/Rollback это no-op.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	ctx = context.WithValue(ctx, txCtxKey{}, tx)

	if err := fn(ctx); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return fmt.Errorf("pgkit: rollback: %w (after: %w)", rbErr, err)
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgkit: commit: %w", err)
	}
	return nil
}
