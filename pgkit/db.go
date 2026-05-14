// Package pgkit — фасад поверх pgx/v5: пул соединений, Transactor,
// migrate-runner, маппинг pg-ошибок в sentinel'ы, health-probe.
// Поддерживает read-replica (PG_REPLICA_DSN) через ReadWriteDB и
// автоматически инструментирует пул метриками db_pool_* (см. metrics.go)
// и per-query спанами через otelpgx.
package pgkit

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type (
	Row        = pgx.Row
	Rows       = pgx.Rows
	Tx         = pgx.Tx
	NamedArgs  = pgx.NamedArgs
	CommandTag = pgconn.CommandTag
)

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
