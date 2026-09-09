package pgkit

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transactor — граница атомарности: fn выполняется в одной транзакции,
// проброшенной через ctx.
type Transactor interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error
}

// DBTX — исполнитель запроса: пул или транзакция.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PoolTransactor — реализация Transactor поверх пула.
type PoolTransactor struct {
	pool *pgxpool.Pool
}

// NewPoolTransactor собирает Transactor поверх пула.
func NewPoolTransactor(pool *pgxpool.Pool) *PoolTransactor {
	return &PoolTransactor{pool: pool}
}

type txCtxKey struct{}

// testTx — маркер транзакции без pgx.Tx (WithTestTx): InTx его видит, Conn — нет.
type testTx struct{}

// Conn выбирает исполнителя запроса: транзакцию из ctx или сам пул.
func Conn(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if tx, ok := ctx.Value(txCtxKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}

// InTx сообщает, несёт ли ctx активную транзакцию (Transactor.WithTx или WithTestTx).
func InTx(ctx context.Context) bool {
	return ctx.Value(txCtxKey{}) != nil
}

// WithTestTx помечает ctx как «внутри транзакции» для фейков Transactor: код,
// гейтящийся на InTx, проходит без Postgres. Conn такой ctx игнорирует.
func WithTestTx(ctx context.Context) context.Context {
	return context.WithValue(ctx, txCtxKey{}, testTx{})
}

// WithTx открывает транзакцию с уровнем ReadCommitted.
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

	txCtx := context.WithValue(ctx, txCtxKey{}, tx)

	if err := fn(txCtx); err != nil {
		if rbErr := rollbackErr(ctx, tx); rbErr != nil {
			return errors.Join(err, rbErr)
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgkit: commit: %w", err)
	}
	return nil
}

// rollbackErr: при отменённом ctx pgx уже уничтожил соединение, и ошибка отката
// — артефакт отмены, уводящий от настоящей причины.
func rollbackErr(ctx context.Context, tx pgx.Tx) error {
	err := tx.Rollback(context.WithoutCancel(ctx))
	if err == nil || errors.Is(err, pgx.ErrTxClosed) || ctx.Err() != nil {
		return nil //nolint:nilerr // ошибка отката на отменённом ctx — артефакт отмены
	}
	return fmt.Errorf("pgkit: rollback: %w", err)
}
