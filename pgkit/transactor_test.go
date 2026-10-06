package pgkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

var errRollback = errors.New("rollback please")

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

// Вложенный вызов идёт во внешней транзакции: строже её (изоляция выше или ReadOnly внутри
// ReadWrite) — ErrNestedTxStricter до fn, а не молча потерянная защита (KCOR-07). Слабее или
// равный — участвует; маркер WithTestTx опций не знает и не проверяется.
func TestWithTxOptions_NestedStricterIsAnError(t *testing.T) {
	_, tr, _ := startPool(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	ro := pgx.TxOptions{AccessMode: pgx.ReadOnly}
	tests := []struct {
		name          string
		outer, nested pgx.TxOptions
		want          string
	}{
		{
			"serializable in read committed",
			pgx.TxOptions{IsoLevel: pgx.ReadCommitted},
			pgx.TxOptions{IsoLevel: pgx.Serializable},
			"outer read committed read write, nested serializable read write",
		},
		{
			"repeatable read in default",
			pgx.TxOptions{},
			pgx.TxOptions{IsoLevel: pgx.RepeatableRead},
			"outer read committed read write, nested repeatable read read write",
		},
		{
			"serializable in repeatable read",
			pgx.TxOptions{IsoLevel: pgx.RepeatableRead},
			pgx.TxOptions{IsoLevel: pgx.Serializable},
			"outer repeatable read read write, nested serializable read write",
		},
		{
			"read only in read write",
			pgx.TxOptions{IsoLevel: pgx.Serializable},
			pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadOnly},
			"outer serializable read write, nested serializable read only",
		},
		{"read committed in serializable", pgx.TxOptions{IsoLevel: pgx.Serializable}, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, ""},
		{"read uncommitted in default", pgx.TxOptions{}, pgx.TxOptions{IsoLevel: pgx.ReadUncommitted}, ""},
		{"same repeatable read", pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, ""},
		{"read write in read only", ro, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, ""},
		{"read only in read only", ro, ro, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran := false
			err := tr.WithTxOptions(ctx, tt.outer, func(ctx context.Context) error {
				return tr.WithTxOptions(ctx, tt.nested, func(context.Context) error {
					ran = true
					return nil
				})
			})
			if tt.want == "" {
				if err != nil || !ran {
					t.Fatalf("WithTxOptions = %v, ran=%v, want the nested call inside the outer transaction", err, ran)
				}
				return
			}
			if !errors.Is(err, pgkit.ErrNestedTxStricter) || !strings.Contains(err.Error(), tt.want) || ran {
				t.Fatalf("WithTxOptions = %v, ran=%v, want ErrNestedTxStricter with %q before fn", err, ran, tt.want)
			}
		})
	}

	ran := false
	err := tr.WithTxOptions(pgkit.WithTestTx(ctx), pgx.TxOptions{IsoLevel: pgx.Serializable}, func(context.Context) error {
		ran = true
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("WithTxOptions under WithTestTx = %v, ran=%v, want fn without the check", err, ran)
	}
}

// Проглоченная в fn SQL-ошибка не даёт коммита: Postgres держит транзакцию
// aborted, и Commit возвращает ErrTxCommitRollback.
func TestWithTx_SwallowedSQLErrorFailsCommit(t *testing.T) {
	db, tr, _ := startPool(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := tr.WithTx(ctx, func(ctx context.Context) error {
		if _, err := db.Exec(ctx, `INSERT INTO t VALUES (1)`); err != nil {
			return err
		}
		_, _ = db.Exec(ctx, `INSERT INTO no_such_table VALUES (1)`)
		return nil
	})
	if !errors.Is(err, pgx.ErrTxCommitRollback) {
		t.Fatalf("WithTx err = %v, want pgx.ErrTxCommitRollback", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0 (транзакция не закоммичена)", count)
	}
}

func TestWithTx_FnErrorRollsBack(t *testing.T) {
	db, tr, _ := startPool(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sentinel := errors.New("domain failure")
	err := tr.WithTx(ctx, func(ctx context.Context) error {
		if _, err := db.Exec(ctx, `INSERT INTO t VALUES (1)`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx err = %v, want %v", err, sentinel)
	}
	if err.Error() != sentinel.Error() {
		t.Fatalf("WithTx err = %q, want ошибку fn без обёртки", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

// Отмена ctx внутри fn: наружу уходит причина отмены, а не «сломался rollback».
func TestWithTx_CtxCancelKeepsOriginalCause(t *testing.T) {
	db, tr, acquired := startPool(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := tr.WithTx(ctx, func(ctx context.Context) error {
		_, err := db.Exec(ctx, `SELECT pg_sleep(5)`)
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WithTx err = %v, want context.DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), "rollback") {
		t.Fatalf("WithTx err = %q, ошибка отката маскирует причину", err)
	}

	// MaxConns=1: слот должен вернуться в пул, иначе следующий WithTx не пройдёт.
	deadline := time.Now().Add(10 * time.Second)
	for acquired() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := acquired(); n != 0 {
		t.Fatalf("после отмены занято соединений: %d, want 0", n)
	}
	next, cancelNext := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelNext()
	if err := tr.WithTx(next, func(ctx context.Context) error {
		_, err := db.Exec(ctx, `INSERT INTO t VALUES (7)`)
		return err
	}); err != nil {
		t.Fatalf("WithTx после отмены: %v", err)
	}
}

func TestInTx_TestMarker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if pgkit.InTx(ctx) {
		t.Fatal("InTx(background) = true")
	}
	if !pgkit.InTx(pgkit.WithTestTx(ctx)) {
		t.Fatal("InTx(WithTestTx) = false")
	}
}

// Настоящая (не артефакт отмены) ошибка отката присоединяется к причине через
// errors.Join и не подменяет её.
func TestWithTx_JoinsRealRollbackError(t *testing.T) {
	db, tr, acquired := startPool(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sentinel := errors.New("domain failure")
	err := tr.WithTx(ctx, func(ctx context.Context) error {
		// Backend убивает сам себя: откат уходит в мёртвое соединение, а ctx жив.
		_, _ = db.Exec(ctx, `SELECT pg_terminate_backend(pg_backend_pid())`)
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx err = %v, want errors.Is sentinel", err)
	}
	if !strings.Contains(err.Error(), "pgkit: rollback") {
		t.Fatalf("WithTx err = %q, want ошибку отката рядом с причиной", err)
	}
	if ctx.Err() != nil {
		t.Fatal("ctx отменился — тест проверял бы артефакт отмены")
	}

	deadline := time.Now().Add(10 * time.Second)
	for acquired() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := acquired(); n != 0 {
		t.Fatalf("после сбоя отката занято соединений: %d, want 0", n)
	}
}
