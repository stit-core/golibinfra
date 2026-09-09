package pgkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestDB_QueryUsesTxFromCtx(t *testing.T) {
	db, tr, _ := startPool(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Строка видна через Query только внутри своей транзакции.
	err := tr.WithTx(ctx, func(ctx context.Context) error {
		if _, err := db.Exec(ctx, `INSERT INTO t VALUES (42)`); err != nil {
			return err
		}
		rows, err := db.Query(ctx, `SELECT id FROM t ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()

		var ids []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) != 1 || ids[0] != 42 {
			t.Fatalf("ids = %v, want [42]", ids)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("WithTx err = %v, want errRollback", err)
	}

	rows, err := db.Query(ctx, `SELECT id FROM t`)
	if err != nil {
		t.Fatalf("Query после отката: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("строка пережила откат")
	}
}

func TestProbe_LiveAndClosedPool(t *testing.T) {
	pg := testkit.StartPostgres(t)
	pool, err := pgkit.NewPool(context.Background(), pg.PoolConfig())
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	probe := pgkit.Probe(pgkit.NewDB(pool))
	if probe.Name != "pgkit" {
		t.Fatalf("probe.Name = %q, want pgkit", probe.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := probe.Run(ctx); err != nil {
		t.Fatalf("Probe на живом пуле: %v", err)
	}

	pool.Close()
	if err := probe.Run(ctx); err == nil {
		t.Fatal("Probe на закрытом пуле вернула nil")
	}
}

func TestReplicaProbe_NoReplicaPasses(t *testing.T) {
	t.Parallel()
	master := pgkit.NewDB(nil)
	probe := pgkit.ReplicaProbe(pgkit.NewReadWriteDB(master, nil))
	if probe.Name != "pgkit/replica" {
		t.Fatalf("probe.Name = %q, want pgkit/replica", probe.Name)
	}
	if err := probe.Run(context.Background()); err != nil {
		t.Fatalf("ReplicaProbe без реплики: %v", err)
	}
}

// Упавшая реплика валит ReplicaProbe, а не проходит молча.
func TestReplicaProbe_DownReplicaFails(t *testing.T) {
	pg := testkit.StartPostgres(t)
	replicaPool, err := pgkit.NewPool(context.Background(), pg.PoolConfig())
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	rw := pgkit.NewReadWriteDB(pg.DB(t), pgkit.NewDB(replicaPool))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	probe := pgkit.ReplicaProbe(rw)
	if err := probe.Run(ctx); err != nil {
		t.Fatalf("ReplicaProbe на живой реплике: %v", err)
	}

	replicaPool.Close()
	err = probe.Run(ctx)
	if err == nil {
		t.Fatal("ReplicaProbe на упавшей реплике вернула nil")
	}
	if !strings.Contains(err.Error(), "pgkit: replica ping") {
		t.Fatalf("ReplicaProbe err = %q, want pgkit: replica ping", err)
	}
	// Мастер тем же ReadWriteDB остаётся доступен: пробы независимы.
	if err := pgkit.Probe(rw.Write()).Run(ctx); err != nil {
		t.Fatalf("Probe мастера: %v", err)
	}
}
