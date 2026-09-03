package pgkit_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func startPool(t *testing.T, maxConns int32) (*pgkit.DB, *pgkit.PoolTransactor, func() int32) {
	t.Helper()
	pg := testkit.StartPostgres(t)
	pool, err := pgkit.NewPool(context.Background(), pgkit.PoolConfig{
		DSN: pg.DSN, MaxConns: maxConns, MinConns: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, HealthCheck: time.Minute,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	db := pgkit.NewDB(pool)
	if _, err := db.Exec(context.Background(), `CREATE TABLE t (id INT PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db, pgkit.NewPoolTransactor(pool), func() int32 { return pool.Stat().AcquiredConns() }
}

func TestWithTx_PanicRollsBackAndReleasesConn(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	db, tr, acquired := startPool(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("panic должен пробрасываться из WithTx")
			}
		}()
		_ = tr.WithTx(ctx, func(ctx context.Context) error {
			if _, err := db.Exec(ctx, `INSERT INTO t VALUES (1)`); err != nil {
				return err
			}
			panic("boom")
		})
	}()

	if n := acquired(); n != 0 {
		t.Fatalf("после panic занято соединений: %d, want 0", n)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("строка пережила panic: count=%d", count)
	}
	// MaxConns=1: без освобождённого слота следующая транзакция зависла бы.
	if err := tr.WithTx(ctx, func(ctx context.Context) error {
		_, err := db.Exec(ctx, `INSERT INTO t VALUES (2)`)
		return err
	}); err != nil {
		t.Fatalf("WithTx after panic: %v", err)
	}
}

func TestWithTxOptions_IsolationAndNesting(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	db, tr, _ := startPool(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := tr.WithTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(ctx context.Context) error {
		var iso string
		if err := db.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&iso); err != nil {
			return err
		}
		if iso != "serializable" {
			t.Fatalf("isolation = %q, want serializable", iso)
		}
		var outer int64
		if err := db.QueryRow(ctx, `SELECT txid_current()`).Scan(&outer); err != nil {
			return err
		}
		return tr.WithTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context) error {
			if !pgkit.InTx(ctx) {
				t.Fatal("вложенный вызов потерял транзакцию")
			}
			var inner int64
			if err := db.QueryRow(ctx, `SELECT txid_current()`).Scan(&inner); err != nil {
				return err
			}
			if inner != outer {
				t.Fatalf("вложенный WithTxOptions открыл новую транзакцию: %d != %d", inner, outer)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("WithTxOptions: %v", err)
	}
}
