package pgkit

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Type-aliases чтобы продуктовый код не импортил pgx/pgconn напрямую.
type (
	Row        = pgx.Row
	Rows       = pgx.Rows
	Tx         = pgx.Tx
	NamedArgs  = pgx.NamedArgs
	CommandTag = pgconn.CommandTag
)

// DB — публичный фасад поверх pool'а. Репозитории получают *pgkit.DB
// и не знают про pgxpool. Внутри маршрутизирует tx-из-ctx (если идёт
// через Transactor.WithTx) или работает напрямую с пулом.
type DB struct {
	pool *pgxpool.Pool
}

func NewDB(pool *pgxpool.Pool) *DB { return &DB{pool: pool} }

func (d *DB) Exec(ctx context.Context, sql string, args ...any) (CommandTag, error) {
	return Conn(ctx, d.pool).Exec(ctx, sql, args...)
}

func (d *DB) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return Conn(ctx, d.pool).Query(ctx, sql, args...)
}

func (d *DB) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return Conn(ctx, d.pool).QueryRow(ctx, sql, args...)
}
