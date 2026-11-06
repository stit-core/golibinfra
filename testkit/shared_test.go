package testkit

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// writeShared — файл общих серверов, как его пишет вызывающий, и каталог замков; путь — для SharedEnv.
func writeShared(t *testing.T, s sharedServers) string {
	t.Helper()
	dir := t.TempDir()
	s.Locks = filepath.Join(dir, "locks")
	if err := os.Mkdir(s.Locks, 0o700); err != nil {
		t.Fatalf("locks: %v", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, "servers.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func itemsSource(sql string) pgkit.Source {
	return pgkit.Source{Name: "items", Dir: "m", FS: fstest.MapFS{
		"m/0001_items.up.sql":   {Data: []byte(sql)},
		"m/0001_items.down.sql": {Data: []byte(`DROP TABLE items;`)},
	}}
}

func databases(t *testing.T, admin *pgkit.DB, like string) []string {
	t.Helper()
	rows, err := admin.Query(t.Context(), `SELECT datname FROM pg_database WHERE datname LIKE $1 ORDER BY datname`, like)
	if err != nil {
		t.Fatalf("list databases: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return names
}

func databaseOf(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	return strings.TrimPrefix(u.Path, "/")
}

// TestShared_Postgres: на общем Postgres у теста своя база — клон шаблона миграций: таблица миграции на месте, запись
// прошлого теста не видна, база снимается после теста; шаблон один на набор миграций, другой набор — свой шаблон, без
// миграций — пустая база.
func TestShared_Postgres(t *testing.T) {
	srv := StartPostgres(t)
	t.Setenv(SharedEnv, writeShared(t, sharedServers{Postgres: &sharedServer{Image: PostgresImage, URL: srv.DSN}}))
	admin := srv.DB(t)
	items := itemsSource(`CREATE TABLE items (id INT PRIMARY KEY);`)

	var first string
	t.Run("writes", func(t *testing.T) {
		pg := StartPostgres(t, WithMigrations(items))
		if pg.Container != nil {
			t.Fatal("shared mode starts no container")
		}
		first = databaseOf(t, pg.DSN)
		if _, err := pg.DB(t).Exec(t.Context(), `INSERT INTO items VALUES (1)`); err != nil {
			t.Fatalf("insert: %v", err)
		}
	})
	if got := databases(t, admin, first); len(got) != 0 {
		t.Fatalf("database %s outlives its test: %v", first, got)
	}
	t.Run("reads", func(t *testing.T) {
		pg := StartPostgres(t, WithMigrations(items))
		if databaseOf(t, pg.DSN) == first {
			t.Fatal("two tests share a database")
		}
		var n int
		if err := pg.DB(t).QueryRow(t.Context(), `SELECT count(*) FROM items`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("items of the previous test: %d, %v", n, err)
		}
	})
	if got := databases(t, admin, "tk_%"); len(got) != 1 {
		t.Fatalf("one template per migration set, got %v", got)
	}
	t.Run("other migrations", func(t *testing.T) {
		pg := StartPostgres(t, WithMigrations(itemsSource(`CREATE TABLE items (id BIGINT PRIMARY KEY);`)))
		var typ string
		if err := pg.DB(t).QueryRow(t.Context(),
			`SELECT data_type FROM information_schema.columns WHERE table_name = 'items'`).Scan(&typ); err != nil || typ != "bigint" {
			t.Fatalf("column type %q, %v: a changed migration needs its own template", typ, err)
		}
	})
	if got := databases(t, admin, "tk_%"); len(got) != 2 {
		t.Fatalf("templates %v, want 2", got)
	}
	t.Run("no migrations", func(t *testing.T) {
		pg := StartPostgres(t)
		var n int
		if err := pg.DB(t).QueryRow(t.Context(),
			`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("tables of an empty database: %d, %v", n, err)
		}
	})
}

// TestShared_PostgresClonesInParallel: клоны одного шаблона из параллельных тестов — разные базы без отказов.
func TestShared_PostgresClonesInParallel(t *testing.T) {
	srv := StartPostgres(t)
	admin, err := adminPool(srv.DSN)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	tmpl, err := templateDB(t.Context(), admin, srv.DSN, []pgkit.Source{itemsSource(`CREATE TABLE items (id INT PRIMARY KEY);`)})
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	const n = 8
	names := make(chan string, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			name, err := cloneDB(t.Context(), admin, tmpl)
			if err != nil {
				errs <- err
				return
			}
			names <- name
		})
	}
	wg.Wait()
	close(names)
	close(errs)
	for err := range errs {
		t.Errorf("clone: %v", err)
	}
	seen := map[string]bool{}
	for name := range names {
		seen[name] = true
		if err := dropDB(admin, name); err != nil {
			t.Errorf("drop: %v", err)
		}
	}
	if len(seen) != n {
		t.Fatalf("distinct databases %d, want %d", len(seen), n)
	}
}

// TestShared_PostgresTemplateInParallel: шаблон нового набора миграций из параллельных тестов — один шаблон и ни
// одного отказа: держатель замка достраивает шаблон своим соединением, ждущие пул не выбирают.
func TestShared_PostgresTemplateInParallel(t *testing.T) {
	srv := StartPostgres(t)
	admin, err := adminPool(srv.DSN)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	src := []pgkit.Source{itemsSource(`CREATE TABLE items (id INT PRIMARY KEY, note TEXT);`)}
	const n = 12
	names := make(chan string, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			name, err := templateDB(t.Context(), admin, srv.DSN, src)
			if err != nil {
				errs <- err
				return
			}
			names <- name
		})
	}
	wg.Wait()
	close(names)
	close(errs)
	for err := range errs {
		t.Errorf("template: %v", err)
	}
	seen := map[string]bool{}
	for name := range names {
		seen[name] = true
	}
	if len(seen) != 1 {
		t.Fatalf("templates %v, want one", seen)
	}
	if got := databases(t, srv.DB(t), "tk_%"); len(got) != 1 {
		t.Fatalf("templates on the server %v, want one", got)
	}
}

// TestShared_PostgresDropsOpenDatabase: база теста снимается и с живым соединением — пул, который тест не закрыл,
// снятие не держит.
func TestShared_PostgresDropsOpenDatabase(t *testing.T) {
	srv := StartPostgres(t)
	admin, err := adminPool(srv.DSN)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	name, err := cloneDB(t.Context(), admin, "template1")
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	dsn, err := databaseURL(srv.DSN, name)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if err := dropDB(admin, name); err != nil {
		t.Fatalf("drop with an open connection: %v", err)
	}
	if got := databases(t, srv.DB(t), name); len(got) != 0 {
		t.Fatalf("database %s stays: %v", name, got)
	}
}

// TestShared_PostgresBrokenMigration: битая миграция — ошибка миграции, как у контейнера на вызов, и недостроенного
// шаблона на сервере не остаётся.
func TestShared_PostgresBrokenMigration(t *testing.T) {
	srv := StartPostgres(t)
	admin, err := adminPool(srv.DSN)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	_, err = templateDB(t.Context(), admin, srv.DSN, []pgkit.Source{itemsSource(`CREATE TABLE (`)})
	if err == nil || !strings.Contains(err.Error(), "testkit: migrate up") {
		t.Fatalf("err = %v, want the migration error", err)
	}
	if got := databases(t, srv.DB(t), "tk_%"); len(got) != 0 {
		t.Fatalf("a broken template stays on the server: %v", got)
	}
}

func natsStreams(t *testing.T, srv string) []string {
	t.Helper()
	nc, err := nats.Connect(srv)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	lister := js.StreamNames(t.Context())
	var names []string
	for name := range lister.Name() {
		names = append(names, name)
	}
	if err := lister.Err(); err != nil {
		t.Fatalf("streams: %v", err)
	}
	return names
}

// TestShared_NATS: сервер NATS с JetStream из пула — у одного теста за раз и без потоков прошлого теста; пока аренда
// держится, другой тест получает другой сервер; NATS без JetStream — свой контейнер.
func TestShared_NATS(t *testing.T) {
	a, b := StartNATS(t, WithJetStream()), StartNATS(t, WithJetStream())
	t.Setenv(SharedEnv, writeShared(t, sharedServers{NATS: []sharedServer{{NATSImage, a.URL}, {NATSImage, b.URL}}}))

	var used string
	t.Run("stream", func(t *testing.T) {
		n := StartNATS(t, WithJetStream())
		if n.Container != nil {
			t.Fatal("shared mode starts no container")
		}
		used = n.URL
		nc, err := nats.Connect(n.URL)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer nc.Close()
		js, err := jetstream.New(nc)
		if err != nil {
			t.Fatalf("jetstream: %v", err)
		}
		if _, err := js.CreateStream(t.Context(), jetstream.StreamConfig{Name: "ORDERS", Subjects: []string{"orders.>"}}); err != nil {
			t.Fatalf("stream: %v", err)
		}
		t.Run("held", func(t *testing.T) {
			if m := StartNATS(t, WithJetStream()); m.URL == n.URL {
				t.Fatal("a leased server goes to a second test")
			}
		})
	})
	if got := natsStreams(t, used); len(got) != 0 {
		t.Fatalf("streams outlive the test: %v", got)
	}
	t.Run("clean", func(t *testing.T) {
		if got := natsStreams(t, StartNATS(t, WithJetStream()).URL); len(got) != 0 {
			t.Fatalf("streams of the previous test: %v", got)
		}
	})
	t.Run("core", func(t *testing.T) {
		if StartNATS(t).Container == nil {
			t.Fatal("NATS without JetStream is its own container")
		}
	})
}

// TestShared_Redis: сервер Redis из пула — без ключей прошлого теста.
func TestShared_Redis(t *testing.T) {
	r := StartRedis(t)
	t.Setenv(SharedEnv, writeShared(t, sharedServers{Redis: []sharedServer{{RedisImage, r.URL}}}))
	client := func(t *testing.T, srv string) *redis.Client {
		t.Helper()
		opt, err := redis.ParseURL(srv)
		if err != nil {
			t.Fatalf("url: %v", err)
		}
		c := redis.NewClient(opt)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	t.Run("set", func(t *testing.T) {
		fix := StartRedis(t)
		if fix.Container != nil {
			t.Fatal("shared mode starts no container")
		}
		if err := client(t, fix.URL).Set(t.Context(), "k", "v", 0).Err(); err != nil {
			t.Fatalf("set: %v", err)
		}
	})
	t.Run("clean", func(t *testing.T) {
		if n, err := client(t, StartRedis(t).URL).DBSize(t.Context()).Result(); err != nil || n != 0 {
			t.Fatalf("keys of the previous test: %d, %v", n, err)
		}
	})
}

// TestShared_MinIO: сервер MinIO из пула — без бакетов прошлого теста.
func TestShared_MinIO(t *testing.T) {
	m := StartMinIO(t)
	t.Setenv(SharedEnv, writeShared(t, sharedServers{MinIO: []minioServer{{
		Image: MinIOImage, Endpoint: m.Endpoint, AccessKey: m.AccessKey, SecretKey: m.SecretKey,
	}}}))
	client := func(t *testing.T, fix *MinIOFixture) *miniogo.Client {
		t.Helper()
		c, err := miniogo.New(fix.Endpoint, &miniogo.Options{Creds: credentials.NewStaticV4(fix.AccessKey, fix.SecretKey, "")})
		if err != nil {
			t.Fatalf("client: %v", err)
		}
		return c
	}
	t.Run("bucket", func(t *testing.T) {
		fix := StartMinIO(t)
		if fix.Container != nil {
			t.Fatal("shared mode starts no container")
		}
		c := client(t, fix)
		if err := c.MakeBucket(t.Context(), "files", miniogo.MakeBucketOptions{}); err != nil {
			t.Fatalf("bucket: %v", err)
		}
		if _, err := c.PutObject(t.Context(), "files", "a.txt", strings.NewReader("a"), 1, miniogo.PutObjectOptions{}); err != nil {
			t.Fatalf("put: %v", err)
		}
	})
	t.Run("clean", func(t *testing.T) {
		buckets, err := client(t, StartMinIO(t)).ListBuckets(t.Context())
		if err != nil || len(buckets) != 0 {
			t.Fatalf("buckets of the previous test: %v, %v", buckets, err)
		}
	})
}

// TestShared_File: без SharedEnv — контейнер на вызов; битый файл серверов и пул без каталога замков — отказ теста,
// а не тихий контейнер.
func TestShared_File(t *testing.T) {
	t.Setenv(SharedEnv, "")
	if s := shared(&fatalTB{}); s != nil {
		t.Fatalf("shared without %s: %+v", SharedEnv, s)
	}
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	noLocks := filepath.Join(dir, "nolocks.json")
	if err := os.WriteFile(noLocks, []byte(`{"redis":[{"image":"redis:7-alpine","url":"redis://127.0.0.1:1"}]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, path := range []string{filepath.Join(dir, "missing.json"), broken, noLocks} {
		t.Setenv(SharedEnv, path)
		tb := &fatalTB{}
		if s := shared(tb); s != nil || len(tb.msgs) != 1 || !strings.Contains(tb.msgs[0], SharedEnv) {
			t.Fatalf("%s: shared = %+v, fatals = %v", path, s, tb.msgs)
		}
	}
}

// TestLease: сервер пула — у одного держателя замка: заняты все — свободного нет, закрытый замок отпускает сервер.
func TestLease(t *testing.T) {
	dir := t.TempDir()
	i, a, err := tryLease(dir, "x", 2)
	if err != nil || a == nil || i != 0 {
		t.Fatalf("first lease: %d, %v, %v", i, a, err)
	}
	j, b, err := tryLease(dir, "x", 2)
	if err != nil || b == nil || j != 1 {
		t.Fatalf("second lease: %d, %v, %v", j, b, err)
	}
	if _, c, err := tryLease(dir, "x", 2); err != nil || c != nil {
		t.Fatalf("a third holder of two servers: %v, %v", c, err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	k, c, err := tryLease(dir, "x", 2)
	if err != nil || c == nil || k != 0 {
		t.Fatalf("lease after release: %d, %v, %v", k, c, err)
	}
	_ = b.Close()
	_ = c.Close()
	if _, _, err := tryLease(filepath.Join(dir, "missing"), "x", 1); err == nil {
		t.Fatal("a lock directory that does not exist is an error")
	}
}
