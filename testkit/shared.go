package testkit

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// SharedEnv — переменная окружения с путём к JSON общих серверов прогона: Start* берут у них свою базу или сервер в
// аренду вместо своего контейнера (README кита § Общий режим). Без переменной — контейнер на вызов.
const SharedEnv = "TESTKIT_SHARED"

// sharedTimeout — предел операций общего режима: клон базы, сборка шаблона без миграций, очистка сервера.
const sharedTimeout = 30 * time.Second

// sharedServers — файл по пути из SharedEnv: серверы поднимает и снимает вызывающий.
type sharedServers struct {
	Locks    string         `json:"locks"`
	Postgres *sharedServer  `json:"postgres"`
	NATS     []sharedServer `json:"nats"`
	Redis    []sharedServer `json:"redis"`
	MinIO    []minioServer  `json:"minio"`
}

type sharedServer struct {
	Image string `json:"image"`
	URL   string `json:"url"`
}

type minioServer struct {
	Image     string `json:"image"`
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// shared — общие серверы прогона из SharedEnv или nil. Битый файл — отказ теста, а не тихий контейнер на вызов:
// вызывающий ждёт общего режима.
func shared(t testing.TB) *sharedServers {
	t.Helper()
	path := os.Getenv("TESTKIT_SHARED") // SharedEnv литералом: переменные кода сверяет с доками envdoc-check
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // G703: путь — из SharedEnv вызывающего, его же файл серверов
	if err != nil {
		t.Fatalf("testkit: %s: %v", SharedEnv, err)
		return nil
	}
	var s sharedServers
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("testkit: %s: %s: %v", SharedEnv, path, err)
		return nil
	}
	if s.Locks == "" && (len(s.NATS) > 0 || len(s.Redis) > 0 || len(s.MinIO) > 0) {
		t.Fatalf("testkit: %s: %s: locks is empty", SharedEnv, path)
		return nil
	}
	return &s
}

// samePool — пул общих серверов образа image; другой образ — контейнер на вызов.
func samePool[S interface{ image() string }](pool []S, image string) bool {
	return len(pool) > 0 && pool[0].image() == image
}

func (s sharedServer) image() string { return s.Image }
func (s minioServer) image() string  { return s.Image }

// admins — пул суперпользователя на общий Postgres: один на процесс и URL, живёт до конца процесса.
var admins sync.Map

func adminPool(url string) (*pgxpool.Pool, error) {
	if p, ok := admins.Load(url); ok {
		return p.(*pgxpool.Pool), nil
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("testkit: shared postgres: %w", err)
	}
	// Ждущие шаблон держат по соединению на замке: запас — клонам и снятию баз соседних тестов процесса.
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("testkit: shared postgres: %w", err)
	}
	if p, loaded := admins.LoadOrStore(url, pool); loaded {
		pool.Close()
		return p.(*pgxpool.Pool), nil
	}
	return pool, nil
}

// sharedPostgres — своя база теста на общем Postgres: клон шаблона с миграциями опций (без миграций — template1, как у
// новой базы контейнера), снимается в t.Cleanup.
func sharedPostgres(t testing.TB, srv sharedServer, o options) *PostgresFixture {
	t.Helper()
	admin, err := adminPool(srv.URL)
	if err != nil {
		t.Fatalf("%v", err)
		return nil
	}
	tmpl := "template1"
	if len(o.sources) > 0 {
		if tmpl, err = templateDB(t.Context(), admin, srv.URL, o.sources); err != nil {
			t.Fatalf("%v", err)
			return nil
		}
	}
	name, err := cloneDB(t.Context(), admin, tmpl)
	if err != nil {
		t.Fatalf("%v", err)
		return nil
	}
	t.Cleanup(func() {
		if err := dropDB(admin, name); err != nil {
			t.Errorf("%v", err)
		}
	})
	dsn, err := databaseURL(srv.URL, name)
	if err != nil {
		t.Fatalf("%v", err)
		return nil
	}
	return &PostgresFixture{DSN: dsn, owner: t}
}

// templateDB — база-шаблон набора миграций sources: собирается один раз на сервер под advisory lock, имя — хеш
// источников; упавшая сборка снимает недостроенную базу и отдаёт ошибку миграции.
func templateDB(ctx context.Context, admin *pgxpool.Pool, url string, sources []pgkit.Source) (string, error) {
	sum, err := sourcesSum(sources)
	if err != nil {
		return "", err
	}
	name := "tk_" + hex.EncodeToString(sum[:12])
	conn, err := admin.Acquire(ctx)
	if err != nil {
		return "", fmt.Errorf("testkit: shared postgres: %w", err)
	}
	defer conn.Release()
	key := int64(binary.BigEndian.Uint64(sum[:8])) //nolint:gosec // G115: ключ advisory lock — биты хеша, знак не важен
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return "", fmt.Errorf("testkit: shared postgres: lock template: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", key) }()
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return "", fmt.Errorf("testkit: shared postgres: %w", err)
	}
	if exists {
		return name, nil
	}
	// Сборка — соединением держателя замка: пул заняли ждущие этот же шаблон, своё соединение из пула он бы не дождался.
	build := name + "_build"
	if err := dropDB(conn, build); err != nil {
		return "", err
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{build}.Sanitize()); err != nil {
		return "", fmt.Errorf("testkit: shared postgres: create template: %w", err)
	}
	dsn, err := databaseURL(url, build)
	if err != nil {
		return "", err
	}
	migCtx, cancel := context.WithTimeout(ctx, migrateTimeout)
	defer cancel()
	if err := pgkit.MigrateUpAll(migCtx, dsn, migrateTimeout, sources...); err != nil {
		return "", errors.Join(fmt.Errorf("testkit: migrate up: %w", err), dropDB(conn, build))
	}
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{build}.Sanitize()+" RENAME TO "+pgx.Identifier{name}.Sanitize()); err != nil {
		return "", fmt.Errorf("testkit: shared postgres: rename template: %w", err)
	}
	return name, nil
}

// sourcesSum — хеш источников в порядке применения: имя, каталог и каждый файл каталога с содержимым.
func sourcesSum(sources []pgkit.Source) ([32]byte, error) {
	h := sha256.New()
	for _, src := range sources {
		fmt.Fprintf(h, "source %q %q\n", src.Name, src.Dir)
		err := fs.WalkDir(src.FS, src.Dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(src.FS, p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "file %q %d\n", p, len(data))
			_, err = h.Write(data)
			return err
		})
		if err != nil {
			return [32]byte{}, fmt.Errorf("testkit: shared postgres: source %s: %w", src.Name, err)
		}
	}
	return [32]byte(h.Sum(nil)), nil
}

// cloneDB — новая база теста из шаблона tmpl.
func cloneDB(ctx context.Context, admin *pgxpool.Pool, tmpl string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("testkit: shared postgres: %w", err)
	}
	name := "t_" + hex.EncodeToString(b[:])
	ctx, cancel := context.WithTimeout(ctx, sharedTimeout)
	defer cancel()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{tmpl}.Sanitize()); err != nil {
		return "", fmt.Errorf("testkit: shared postgres: create database: %w", err)
	}
	return name, nil
}

// execer — пул или соединение суперпользователя.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// dropDB снимает базу с её соединениями: тест, не закрывший пул, не держит базу.
func dropDB(admin execer, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), sharedTimeout)
	defer cancel()
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("testkit: shared postgres: drop database %s: %w", name, err)
	}
	return nil
}

// databaseURL — URL сервера с базой name.
func databaseURL(raw, name string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("testkit: shared postgres: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// sharedNATS — сервер NATS с JetStream в аренду теста, без потоков (KV и object store — тоже потоки); после теста
// потоки снимаются, аренда отпускается.
func sharedNATS(t testing.TB, s *sharedServers) *NATSFixture {
	t.Helper()
	srv := s.NATS[lease(t, s.Locks, "nats", len(s.NATS))].URL
	if err := wipeNATS(t.Context(), srv); err != nil {
		t.Fatalf("%v", err)
		return nil
	}
	t.Cleanup(func() {
		if err := wipeNATS(context.Background(), srv); err != nil {
			t.Errorf("%v", err)
		}
	})
	return &NATSFixture{URL: srv}
}

func wipeNATS(ctx context.Context, srv string) error {
	ctx, cancel := context.WithTimeout(ctx, sharedTimeout)
	defer cancel()
	nc, err := nats.Connect(srv, nats.Timeout(sharedTimeout))
	if err != nil {
		return fmt.Errorf("testkit: shared nats: %w", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("testkit: shared nats: %w", err)
	}
	names := js.StreamNames(ctx)
	var streams []string
	for name := range names.Name() {
		streams = append(streams, name)
	}
	if err := names.Err(); err != nil {
		return fmt.Errorf("testkit: shared nats: list streams: %w", err)
	}
	for _, name := range streams {
		if err := js.DeleteStream(ctx, name); err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
			return fmt.Errorf("testkit: shared nats: delete stream %s: %w", name, err)
		}
	}
	return nil
}

// sharedRedis — сервер Redis в аренду теста, пустой (FLUSHALL до и после теста).
func sharedRedis(t testing.TB, s *sharedServers) *RedisFixture {
	t.Helper()
	srv := s.Redis[lease(t, s.Locks, "redis", len(s.Redis))].URL
	if err := flushRedis(t.Context(), srv); err != nil {
		t.Fatalf("%v", err)
		return nil
	}
	t.Cleanup(func() {
		if err := flushRedis(context.Background(), srv); err != nil {
			t.Errorf("%v", err)
		}
	})
	return &RedisFixture{URL: srv}
}

// flushRedis — FLUSHALL по протоколу RESP: клиент Redis ради одной команды не нужен.
func flushRedis(ctx context.Context, srv string) error {
	u, err := url.Parse(srv)
	if err != nil {
		return fmt.Errorf("testkit: shared redis: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, sharedTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return fmt.Errorf("testkit: shared redis: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write([]byte("*1\r\n$8\r\nFLUSHALL\r\n")); err != nil {
		return fmt.Errorf("testkit: shared redis: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("testkit: shared redis: %w", err)
	}
	if strings.TrimSpace(line) != "+OK" {
		return fmt.Errorf("testkit: shared redis: FLUSHALL: %s", strings.TrimSpace(line))
	}
	return nil
}

// sharedMinIO — сервер MinIO в аренду теста, без бакетов: до и после теста бакеты снимаются с объектами.
func sharedMinIO(t testing.TB, s *sharedServers) *MinIOFixture {
	t.Helper()
	srv := s.MinIO[lease(t, s.Locks, "minio", len(s.MinIO))]
	if err := wipeMinIO(t.Context(), srv); err != nil {
		t.Fatalf("%v", err)
		return nil
	}
	t.Cleanup(func() {
		if err := wipeMinIO(context.Background(), srv); err != nil {
			t.Errorf("%v", err)
		}
	})
	return &MinIOFixture{Endpoint: srv.Endpoint, AccessKey: srv.AccessKey, SecretKey: srv.SecretKey}
}

func wipeMinIO(ctx context.Context, srv minioServer) error {
	ctx, cancel := context.WithTimeout(ctx, sharedTimeout)
	defer cancel()
	client, err := miniogo.New(srv.Endpoint, &miniogo.Options{Creds: credentials.NewStaticV4(srv.AccessKey, srv.SecretKey, "")})
	if err != nil {
		return fmt.Errorf("testkit: shared minio: %w", err)
	}
	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		return fmt.Errorf("testkit: shared minio: list buckets: %w", err)
	}
	for _, b := range buckets {
		if err := client.RemoveBucketWithOptions(ctx, b.Name, miniogo.RemoveBucketOptions{ForceDelete: true}); err != nil {
			return fmt.Errorf("testkit: shared minio: remove bucket %s: %w", b.Name, err)
		}
	}
	return nil
}
