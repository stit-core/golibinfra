package pgstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// monthStart — первое число месяца: AddDate нормализует 31-е число, и тест,
// считающий месяцы от «сегодня», 31-го числа ждал бы партицию через два месяца.
func monthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func monthsFromNow(n int) time.Time { return monthStart(time.Now().UTC()).AddDate(0, n, 0) }

func TestPartitionSQL_Naming(t *testing.T) {
	name, sql := pgstore.PartitionSQL("outbox_orders", time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))
	if name != "outbox_orders_2026_05" {
		t.Fatalf("name: %s", name)
	}
	if sql == "" {
		t.Fatalf("empty sql")
	}
}

// Имя партиции считается от начала месяца: 31-е число не должно уезжать в
// следующий месяц (Go AddDate нормализует 31 января + 1 месяц в 3 марта).
func TestPartitionSQL_MonthEndDatesStayInMonth(t *testing.T) {
	cases := []struct {
		day  time.Time
		want string
	}{
		{time.Date(2026, 1, 29, 23, 59, 0, 0, time.UTC), "outbox_2026_01"},
		{time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC), "outbox_2026_01"},
		{time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC), "outbox_2026_01"},
		{time.Date(2027, 3, 31, 12, 0, 0, 0, time.UTC), "outbox_2027_03"},
		{time.Date(2026, 8, 31, 23, 30, 0, 0, time.UTC), "outbox_2026_08"},
		{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), "outbox_2026_12"},
	}
	for _, c := range cases {
		name, _ := pgstore.PartitionSQL("outbox", c.day)
		if name != c.want {
			t.Fatalf("%s → %s, want %s", c.day.Format(time.RFC3339), name, c.want)
		}
	}
}

// Партиции «следующих месяцев» тоже считаются от начала месяца.
func TestEnsurePartitions_MonthEndDate(t *testing.T) {
	db, _, ctx := startStore(t)
	clock := clockkit.NewFixed(time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC))
	s := pgstore.NewStore(db, pgstore.WithClock(clock))

	if err := s.EnsurePartitions(ctx, 1); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	for _, name := range []string{"outbox_2026_01", "outbox_2026_02"} {
		if !partitionExists(t, ctx, db, name) {
			t.Fatalf("partition %s missing", name)
		}
	}
	if partitionExists(t, ctx, db, "outbox_2026_03") {
		t.Fatal("EnsurePartitions(ctx, 1) created a partition two months ahead")
	}
}

func TestSchemaSQL_HasBackoffColumn(t *testing.T) {
	sql := pgstore.SchemaSQL("outbox")
	if sql == "" {
		t.Fatal("schema sql is empty")
	}
	if !strings.Contains(sql, "next_attempt_at") {
		t.Fatal("SchemaSQL is out of sync with migrations: no next_attempt_at")
	}
}

func TestStore_DropOldPartitions(t *testing.T) {
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	// Партиция 10 месяцев назад — вне диапазона миграции current..+3.
	oldName, oldSQL := pgstore.PartitionSQL("outbox", monthsFromNow(-10))
	if _, err := db.Exec(ctx, oldSQL); err != nil {
		t.Fatalf("create old partition: %v", err)
	}

	if err := s.DropOldPartitions(ctx, 3); err != nil {
		t.Fatalf("drop old partitions: %v", err)
	}

	if partitionExists(t, ctx, db, oldName) {
		t.Fatalf("старая партиция %s не удалена", oldName)
	}
	curName, _ := pgstore.PartitionSQL("outbox", time.Now().UTC())
	if !partitionExists(t, ctx, db, curName) {
		t.Fatalf("текущая партиция %s удалена по ошибке", curName)
	}
}

func TestStore_DropOldPartitions_Disabled(t *testing.T) {
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)
	oldName, oldSQL := pgstore.PartitionSQL("outbox", monthsFromNow(-10))
	if _, err := db.Exec(ctx, oldSQL); err != nil {
		t.Fatalf("create old partition: %v", err)
	}
	if err := s.DropOldPartitions(ctx, 0); err != nil {
		t.Fatalf("retention disabled: %v", err)
	}
	if !partitionExists(t, ctx, db, oldName) {
		t.Fatal("retention disabled, but partition was dropped")
	}
}

// DETACH CONCURRENTLY нельзя выполнить в transaction block — вызов из
// транзакции отвергается сразу, а не падает на сервере.
func TestStore_DropOldPartitions_RejectsTransaction(t *testing.T) {
	s := pgstore.NewStore(nil)
	if err := s.DropOldPartitions(pgkit.WithTestTx(context.Background()), 3); err == nil {
		t.Fatal("expected error when called inside a transaction")
	}
}

// Партиция с неопубликованными событиями переживает retention; остальные старые дропаются.
func TestStore_DropOldPartitions_KeepsPending(t *testing.T) {
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	pendingMonth := monthsFromNow(-10)
	pendingName, pendingSQL := pgstore.PartitionSQL("outbox", pendingMonth)
	if _, err := db.Exec(ctx, pendingSQL); err != nil {
		t.Fatalf("create pending partition: %v", err)
	}
	emptyName, emptySQL := pgstore.PartitionSQL("outbox", monthsFromNow(-9))
	if _, err := db.Exec(ctx, emptySQL); err != nil {
		t.Fatalf("create empty partition: %v", err)
	}
	deadMonth := monthsFromNow(-8)
	deadName, deadSQL := pgstore.PartitionSQL("outbox", deadMonth)
	if _, err := db.Exec(ctx, deadSQL); err != nil {
		t.Fatalf("create dead partition: %v", err)
	}
	inMonth := pendingMonth.AddDate(0, 0, 14)
	if _, err := db.Exec(ctx, `INSERT INTO outbox (subject, payload, created_at) VALUES ('evt', '{}', $1)`, inMonth); err != nil {
		t.Fatalf("insert pending: %v", err)
	}
	deadAt := deadMonth.AddDate(0, 0, 14)
	if _, err := db.Exec(ctx, `INSERT INTO outbox (subject, payload, created_at, dead_at) VALUES ('evt', '{}', $1, $1)`, deadAt); err != nil {
		t.Fatalf("insert dead: %v", err)
	}

	err := s.DropOldPartitions(ctx, 3)
	if !errors.Is(err, pgstore.ErrPendingRows) {
		t.Fatalf("err = %v, want ErrPendingRows", err)
	}
	if !partitionExists(t, ctx, db, pendingName) {
		t.Fatalf("партиция с неопубликованными %s удалена", pendingName)
	}
	if partitionExists(t, ctx, db, emptyName) {
		t.Fatalf("пустая старая партиция %s не удалена", emptyName)
	}
	if partitionExists(t, ctx, db, deadName) {
		t.Fatalf("партиция только с dead-строками %s не удалена", deadName)
	}
}

// Retention отцепляет партицию без ACCESS EXCLUSIVE на родителе: вставки
// доменных транзакций не встают в очередь за DROP'ом.
func TestStore_DropOldPartitions_KeepsWritesFlowing(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	oldName, oldSQL := pgstore.PartitionSQL("outbox", monthsFromNow(-10))
	if _, err := db.Exec(ctx, oldSQL); err != nil {
		t.Fatalf("create old partition: %v", err)
	}

	// Открытая транзакция на родителе заставляет detach ждать её завершения.
	reader := make(chan struct{})
	release := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		readerDone <- tr.WithTx(ctx, func(ctx context.Context) error {
			var n int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
				return err
			}
			close(reader)
			<-release
			return nil
		})
	}()
	<-reader

	dropDone := make(chan error, 1)
	go func() { dropDone <- s.DropOldPartitions(ctx, 3) }()

	// Пока detach ждёт, писатель обязан пройти: с DROP TABLE он встал бы в
	// очередь за ACCESS EXCLUSIVE и упал по lock_timeout.
	writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.Insert(writeCtx, "evt", payload(t, sample{N: 1})); err != nil {
		t.Fatalf("insert blocked by retention: %v", err)
	}

	close(release)
	if err := <-readerDone; err != nil {
		t.Fatalf("reader tx: %v", err)
	}
	if err := <-dropDone; err != nil {
		t.Fatalf("DropOldPartitions: %v", err)
	}
	if partitionExists(t, ctx, db, oldName) {
		t.Fatalf("партиция %s не удалена", oldName)
	}
}

func partitionExists(t *testing.T, ctx context.Context, db *pgkit.DB, name string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname = $1)", name).Scan(&exists); err != nil {
		t.Fatalf("partition exists check: %v", err)
	}
	return exists
}

func TestNewStoreFor_Validation(t *testing.T) {
	if _, err := pgstore.NewStoreFor(nil, "outbox_orders"); err != nil {
		t.Fatalf("valid table rejected: %v", err)
	}
	for _, bad := range []string{"", "Orders", "outbox orders", "1outbox", "outbox;drop"} {
		if _, err := pgstore.NewStoreFor(nil, bad); err == nil {
			t.Fatalf("invalid table %q accepted", bad)
		}
	}
}

func TestStore_PartitionedDrain(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)

	for i := range 5 {
		if err := s.Insert(ctx, "evt.test", payload(t, map[string]int{"i": i})); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	msgs := list(t, tr, s, ctx, 10)
	if len(msgs) != 5 {
		t.Fatalf("want 5 unpublished, got %d", len(msgs))
	}
	for _, m := range msgs {
		if err := s.MarkPublished(ctx, refs(m)); err != nil {
			t.Fatalf("MarkPublished: %v", err)
		}
	}
	if left := list(t, tr, s, ctx, 10); len(left) != 0 {
		t.Fatalf("want 0, got %d", len(left))
	}

	row := db.QueryRow(ctx, "SELECT relkind::text FROM pg_class WHERE relname='outbox'")
	var kind string
	if err := row.Scan(&kind); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if kind != "p" {
		t.Fatalf("expected partitioned table (relkind=p), got %s", kind)
	}
}

func TestStore_EnsurePartitions(t *testing.T) {
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	if err := s.EnsurePartitions(ctx, 6); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	if err := s.EnsurePartitions(ctx, 6); err != nil {
		t.Fatalf("EnsurePartitions idempotent: %v", err)
	}
	if err := s.EnsurePartitions(ctx, -1); err != nil {
		t.Fatalf("EnsurePartitions(-1): %v", err)
	}
	if !partitionExists(t, ctx, db, mustName(monthsFromNow(6))) {
		t.Fatal("partition 6 months ahead missing")
	}
}

// Регрессия: партицию, которую параллельно создаёт другой процесс (хук и
// keeper-job на старте, вторая реплика), EnsurePartitions ждёт и принимает, а не
// падает duplicate_table, — иначе проигравший гонку валил старт приложения.
func TestStore_EnsurePartitions_ConcurrentCreator(t *testing.T) {
	fix, ctx := startFixture(t)
	db := fix.DB(t)
	name, createSQL := pgstore.PartitionSQL("outbox", monthsFromNow(0))
	if _, err := db.Exec(ctx, "DROP TABLE "+name); err != nil {
		t.Fatalf("drop %s: %v", name, err)
	}

	tx, err := fix.Pool(t).Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	if _, err := tx.Exec(ctx, createSQL); err != nil {
		t.Fatalf("create %s in the competing transaction: %v", name, err)
	}

	done := make(chan error, 1)
	go func() { done <- pgstore.NewStore(db).EnsurePartitions(ctx, 0) }()
	waitForLockWait(t, ctx, db, "CREATE TABLE IF NOT EXISTS "+name)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("EnsurePartitions next to a concurrent creator: %v", err)
	}
	if !partitionExists(t, ctx, db, name) {
		t.Fatalf("partition %s missing", name)
	}
}

// waitForLockWait ждёт, пока запрос с префиксом prefix повиснет на блокировке.
func waitForLockWait(t *testing.T, ctx context.Context, db *pgkit.DB, prefix string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var waiting bool
		err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND starts_with(query, $1))`, prefix).Scan(&waiting)
		if err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if waiting {
			return
		}
	}
	t.Fatalf("no query %q waits on a lock", prefix)
}

func mustName(month time.Time) string {
	name, _ := pgstore.PartitionSQL("outbox", month)
	return name
}

// Успевший DETACH с сорвавшимся DROP оставляет таблицу вне pg_inherits;
// retention обязан подобрать её следующим прогоном, а не потерять навсегда.
func TestStore_DropOldPartitions_ReclaimsOrphan(t *testing.T) {
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	orphanName, orphanSQL := pgstore.PartitionSQL("outbox", monthsFromNow(-10))
	if _, err := db.Exec(ctx, orphanSQL); err != nil {
		t.Fatalf("create old partition: %v", err)
	}
	if _, err := db.Exec(ctx, "ALTER TABLE outbox DETACH PARTITION "+orphanName+" CONCURRENTLY"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if attached(t, ctx, db, orphanName) {
		t.Fatal("precondition: партиция должна быть отцеплена")
	}

	if err := s.DropOldPartitions(ctx, 3); err != nil {
		t.Fatalf("DropOldPartitions: %v", err)
	}
	if partitionExists(t, ctx, db, orphanName) {
		t.Fatalf("осиротевшая партиция %s не удалена", orphanName)
	}
}

// Сирота с неопубликованными событиями живёт по тем же правилам, что партиция.
func TestStore_DropOldPartitions_KeepsPendingOrphan(t *testing.T) {
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	month := monthsFromNow(-10)
	orphanName, orphanSQL := pgstore.PartitionSQL("outbox", month)
	if _, err := db.Exec(ctx, orphanSQL); err != nil {
		t.Fatalf("create old partition: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO outbox (subject, payload, created_at) VALUES ('evt', '{}', $1)`,
		month.AddDate(0, 0, 14)); err != nil {
		t.Fatalf("insert pending: %v", err)
	}
	if _, err := db.Exec(ctx, "ALTER TABLE outbox DETACH PARTITION "+orphanName+" CONCURRENTLY"); err != nil {
		t.Fatalf("detach: %v", err)
	}

	if err := s.DropOldPartitions(ctx, 3); !errors.Is(err, pgstore.ErrPendingRows) {
		t.Fatalf("err = %v, want ErrPendingRows", err)
	}
	if !partitionExists(t, ctx, db, orphanName) {
		t.Fatalf("сирота с неопубликованными %s удалена", orphanName)
	}
}

// Чужие таблицы рядом с outbox retention не трогает: имя должно разбираться в
// реальный месяц, а сама таблица — быть партицией или её сиротой.
func TestStore_DropOldPartitions_IgnoresForeignTables(t *testing.T) {
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	for _, name := range []string{"outbox_2020_13", "outbox_backup", "outbox_2019_1"} {
		if _, err := db.Exec(ctx, "CREATE TABLE "+name+" (id INT)"); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	// Партиция с именем не по конвенции кита: месяц из него не выводится, и
	// retention её не трогает, хотя в pg_inherits она есть.
	const handmade = "legacy_outbox_part"
	if _, err := db.Exec(ctx,
		"CREATE TABLE "+handmade+" PARTITION OF outbox FOR VALUES FROM ('2019-01-01') TO ('2019-02-01')"); err != nil {
		t.Fatalf("create %s: %v", handmade, err)
	}
	foreign := []string{"outbox_2020_13", "outbox_backup", "outbox_2019_1", handmade}

	if err := s.DropOldPartitions(ctx, 3); err != nil {
		t.Fatalf("DropOldPartitions: %v", err)
	}
	for _, name := range foreign {
		if !partitionExists(t, ctx, db, name) {
			t.Fatalf("посторонняя таблица %s удалена retention'ом", name)
		}
	}
}

// Прерванный DETACH оставляет партицию в detach-pending; следующий прогон
// завершает её FINALIZE и дропает.
func TestStore_DropOldPartitions_FinalizesInterruptedDetach(t *testing.T) {
	fix, ctx := startFixture(t)
	db := fix.DB(t)

	// Отдельный пул с коротким statement_timeout: detach обрывает сервер, а не
	// клиент, поэтому состояние detach-pending фиксируется детерминированно.
	impatientCfg := fix.PoolConfig()
	impatientCfg.StatementTimeout = 2 * time.Second
	impatientPool, err := pgkit.NewPool(ctx, impatientCfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(impatientPool.Close)

	oldName, oldSQL := pgstore.PartitionSQL("outbox", monthsFromNow(-10))
	if _, err := db.Exec(ctx, oldSQL); err != nil {
		t.Fatalf("create old partition: %v", err)
	}

	// LOCK ONLY держит родителя, но не партицию: первая фаза detach'а проходит
	// и коммитит detach-pending, вторая — ждёт этот lock.
	blocker, err := fix.Pool(t).Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := blocker.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := blocker.Exec(ctx, "LOCK TABLE ONLY outbox IN ACCESS SHARE MODE"); err != nil {
		t.Fatalf("lock parent: %v", err)
	}

	err = pgstore.NewStore(pgkit.NewDB(impatientPool)).DropOldPartitions(ctx, 3)
	if err == nil || !strings.Contains(err.Error(), "detach partition "+oldName) {
		t.Fatalf("DropOldPartitions err = %v, want ошибку detach", err)
	}
	if !detachPending(t, ctx, db, oldName) {
		t.Fatalf("прерванный detach не оставил %s в detach-pending", oldName)
	}

	_, _ = blocker.Exec(ctx, "ROLLBACK")
	blocker.Release()

	if err := pgstore.NewStore(db).DropOldPartitions(ctx, 3); err != nil {
		t.Fatalf("DropOldPartitions после прерванного detach: %v", err)
	}
	if partitionExists(t, ctx, db, oldName) {
		t.Fatalf("партиция %s не удалена через FINALIZE", oldName)
	}
}

// Сорвавшийся DROP отдаётся ошибкой, а не молча теряется.
func TestStore_DropOldPartitions_DropError(t *testing.T) {
	fix, ctx := startFixture(t)
	db := fix.DB(t)
	s := pgstore.NewStore(db)

	oldName, oldSQL := pgstore.PartitionSQL("outbox", monthsFromNow(-10))
	if _, err := db.Exec(ctx, oldSQL); err != nil {
		t.Fatalf("create old partition: %v", err)
	}
	if _, err := db.Exec(ctx, "ALTER TABLE outbox DETACH PARTITION "+oldName+" CONCURRENTLY"); err != nil {
		t.Fatalf("detach: %v", err)
	}

	// Читатель держит ACCESS SHARE на сироте — DROP не получит ACCESS EXCLUSIVE.
	blocker, err := fix.Pool(t).Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := blocker.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := blocker.Exec(ctx, "SELECT count(*) FROM "+oldName); err != nil {
		t.Fatalf("lock orphan: %v", err)
	}

	err = s.DropOldPartitions(ctx, 3)
	if err == nil || !strings.Contains(err.Error(), "drop partition "+oldName) {
		t.Fatalf("DropOldPartitions err = %v, want ошибку drop", err)
	}

	_, _ = blocker.Exec(ctx, "ROLLBACK")
	blocker.Release()
	if !partitionExists(t, ctx, db, oldName) {
		t.Fatalf("партиция %s пропала, хотя DROP не прошёл", oldName)
	}
}

func attached(t *testing.T, ctx context.Context, db *pgkit.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid WHERE c.relname = $1`,
		name).Scan(&n); err != nil {
		t.Fatalf("inherits check: %v", err)
	}
	return n > 0
}

func detachPending(t *testing.T, ctx context.Context, db *pgkit.DB, name string) bool {
	t.Helper()
	var pending bool
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(bool_or(i.inhdetachpending), false)
FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid WHERE c.relname = $1`,
		name).Scan(&pending); err != nil {
		t.Fatalf("detach pending check: %v", err)
	}
	return pending
}
