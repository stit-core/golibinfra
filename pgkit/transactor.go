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

// ErrNestedTxStricter — вложенный WithTxOptions строже внешней транзакции: изоляция выше или
// ReadOnly внутри ReadWrite. Вложенный вызов идёт во внешней транзакции и её защиту не получил бы.
var ErrNestedTxStricter = errors.New("pgkit: nested transaction is stricter than the outer one")

type txCtxKey struct{}

// txState — транзакция ctx и её опции; tx nil — маркер WithTestTx: InTx его видит, Conn — нет.
type txState struct {
	tx   pgx.Tx
	opts pgx.TxOptions
}

// Conn выбирает исполнителя запроса: транзакцию из ctx или сам пул.
func Conn(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if st, ok := ctx.Value(txCtxKey{}).(txState); ok && st.tx != nil {
		return st.tx
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
	return context.WithValue(ctx, txCtxKey{}, txState{})
}

// WithTx открывает транзакцию с уровнем ReadCommitted.
func (t *PoolTransactor) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return t.WithTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, fn)
}

// WithTxOptions открывает транзакцию с заданными опциями; вложенный вызов участвует во внешней
// транзакции, а строже её (изоляция выше или ReadOnly внутри ReadWrite) — ErrNestedTxStricter.
func (t *PoolTransactor) WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error {
	if outer, ok := ctx.Value(txCtxKey{}).(txState); ok {
		if outer.tx != nil {
			if err := checkNested(outer.opts, opts); err != nil {
				return err
			}
		}
		return fn(ctx)
	}

	tx, err := t.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("pgkit: begin tx: %w", err)
	}
	// Откат в defer спасает соединение при panic внутри fn; после Commit/Rollback это no-op.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	txCtx := context.WithValue(ctx, txCtxKey{}, txState{tx: tx, opts: opts})

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

// isoRank — строгость уровня изоляции; пустой — дефолт Postgres, read committed, как и read
// uncommitted.
var isoRank = map[pgx.TxIsoLevel]int{
	"":                  0,
	pgx.ReadUncommitted: 0,
	pgx.ReadCommitted:   0,
	pgx.RepeatableRead:  1,
	pgx.Serializable:    2,
}

func checkNested(outer, nested pgx.TxOptions) error {
	if isoRank[nested.IsoLevel] > isoRank[outer.IsoLevel] ||
		(nested.AccessMode == pgx.ReadOnly && outer.AccessMode != pgx.ReadOnly) {
		return fmt.Errorf("%w: outer %s, nested %s", ErrNestedTxStricter, txMode(outer), txMode(nested))
	}
	return nil
}

func txMode(o pgx.TxOptions) string {
	iso, access := o.IsoLevel, o.AccessMode
	if iso == "" {
		iso = pgx.ReadCommitted
	}
	if access == "" {
		access = pgx.ReadWrite
	}
	return string(iso) + " " + string(access)
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
