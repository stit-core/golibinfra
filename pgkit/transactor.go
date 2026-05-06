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
}

// DBTX — общий минимум pgx.Tx и *pgxpool.Pool. Репо принимает его и не знает,
// крутится ли вокруг транзакция. Достаётся через Conn(ctx, pool).
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

// Conn возвращает активную транзакцию из ctx, иначе — пул.
func Conn(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if tx, ok := ctx.Value(txCtxKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}

// WithTx — вложенные вызовы переиспользуют внешнюю транзакцию.
func (t *PoolTransactor) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txCtxKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}

	tx, err := t.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("pgkit: begin tx: %w", err)
	}

	ctx = context.WithValue(ctx, txCtxKey{}, tx)

	if err := fn(ctx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return fmt.Errorf("pgkit: rollback: %w (after: %w)", rbErr, err)
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgkit: commit: %w", err)
	}
	return nil
}
