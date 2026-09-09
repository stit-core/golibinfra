// Package pgkit — фасад поверх pgx/v5: пул соединений, Transactor,
// migrate-runner, предикаты pg-ошибок, health-проба и read-replica.
package pgkit

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Алиасы pgx-/pgconn-типов: базовые сигнатуры пишутся без импорта pgx.
type (
	// Row — строка результата (pgx.Row).
	Row = pgx.Row
	// Rows — курсор результата (pgx.Rows).
	Rows = pgx.Rows
	// Tx — транзакция (pgx.Tx).
	Tx = pgx.Tx
	// NamedArgs — именованные аргументы запроса (pgx.NamedArgs).
	NamedArgs = pgx.NamedArgs
	// CommandTag — результат выполнения команды (pgconn.CommandTag).
	CommandTag = pgconn.CommandTag
)

// DB — фасад запросов: каждый вызов идёт в транзакцию из ctx, если она есть,
// иначе в пул.
type DB struct {
	pool *pgxpool.Pool
}

// NewDB оборачивает пул в фасад DB.
func NewDB(pool *pgxpool.Pool) *DB { return &DB{pool: pool} }

// Exec выполняет запрос без результата.
func (d *DB) Exec(ctx context.Context, sql string, args ...any) (CommandTag, error) {
	return Conn(ctx, d.pool).Exec(ctx, sql, args...)
}

// Query выполняет запрос, возвращающий строки.
func (d *DB) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return Conn(ctx, d.pool).Query(ctx, sql, args...)
}

// QueryRow выполняет запрос, возвращающий одну строку.
func (d *DB) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return Conn(ctx, d.pool).QueryRow(ctx, sql, args...)
}
