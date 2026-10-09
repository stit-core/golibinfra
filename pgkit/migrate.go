package pgkit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// Source — источник миграций с собственной таблицей версий
// schema_migrations_<Name>: новая миграция в ките не сдвигает версии приложения.
type Source struct {
	// Name — суффикс таблицы версий, ^[a-z][a-z0-9_]{0,44}$: ≤ 45 символов,
	// чтобы schema_migrations_<Name> уложился в 63 байта идентификатора Postgres.
	Name string
	FS   fs.FS  // embed.FS кита или os.DirFS каталога приложения
	Dir  string // каталог внутри FS с *.up.sql/*.down.sql; "." для os.DirFS
}

// DirSource — источник из каталога на диске.
func DirSource(name, path string) Source {
	return Source{Name: name, FS: os.DirFS(path), Dir: "."}
}

// VersionTable — имя таблицы версий источника.
func (s Source) VersionTable() string { return "schema_migrations_" + s.Name }

var sourceNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,44}$`)

func (s Source) validate() error {
	if !sourceNameRe.MatchString(s.Name) {
		return fmt.Errorf("pgkit: migrate: invalid source name %q", s.Name)
	}
	if s.FS == nil {
		return fmt.Errorf("pgkit: migrate: source %q: nil FS", s.Name)
	}
	return nil
}

// MigrateUp применяет все неприменённые миграции источника. Оператор миграции ждёт блокировку
// не дольше lockTimeout (> 0), дедлайн ctx прерывает и идущий оператор; упавшая миграция
// оставляет версию dirty.
func MigrateUp(ctx context.Context, dsn string, lockTimeout time.Duration, src Source) error {
	if err := checkLockTimeout(lockTimeout); err != nil {
		return err
	}
	return withMigrator(ctx, dsn, lockTimeout, src, func(m *migrate.Migrate) error {
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("up: %w", err)
		}
		return nil
	})
}

// MigrateDown откатывает ровно одну миграцию источника; lockTimeout и ctx — как у MigrateUp.
func MigrateDown(ctx context.Context, dsn string, lockTimeout time.Duration, src Source) error {
	if err := checkLockTimeout(lockTimeout); err != nil {
		return err
	}
	return withMigrator(ctx, dsn, lockTimeout, src, func(m *migrate.Migrate) error {
		if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("down: %w", err)
		}
		return nil
	})
}

// MigrateVersion возвращает применённую версию источника (0 — ничего не
// применено); dirty=true приходит вместе с ошибкой.
func MigrateVersion(ctx context.Context, dsn string, src Source) (version uint, dirty bool, err error) {
	// Схему версия не меняет: таблицу версий она создаёт пустой, предел блокировки — дедлайн ctx.
	err = withMigrator(ctx, dsn, 0, src, func(m *migrate.Migrate) error {
		v, d, err := m.Version()
		if errors.Is(err, migrate.ErrNilVersion) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("version: %w", err)
		}
		version, dirty = v, d
		if d {
			return fmt.Errorf("version %d is dirty, manual repair required", v)
		}
		return nil
	})
	return version, dirty, err
}

func checkLockTimeout(d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("%w: migrate: lock timeout must be > 0, got %s", ErrConfig, d)
	}
	return nil
}

// withMigrator: lockTimeout 0 — у MigrateVersion, соединение без lock_timeout.
func withMigrator(ctx context.Context, dsn string, lockTimeout time.Duration, src Source, fn func(*migrate.Migrate) error) error {
	if err := src.validate(); err != nil {
		return err
	}
	srcDrv, err := iofs.New(src.FS, src.Dir)
	if err != nil {
		return fmt.Errorf("pgkit: migrate source %q: %w", src.Name, err)
	}
	if _, err := srcDrv.First(); err != nil {
		return fmt.Errorf("pgkit: migrate source %q: no migrations in %q", src.Name, src.Dir)
	}

	connCfg, err := migrateConnConfig(ctx, dsn)
	if err != nil {
		return err
	}
	if lockTimeout > 0 {
		connCfg.RuntimeParams["lock_timeout"] = strconv.FormatInt(lockTimeout.Milliseconds(), 10)
	}
	// golang-migrate исполняет операторы на context.Background(): дедлайн ctx доходит до них
	// cancel request'ом серверу. Обрыв сокета не помог бы — ALTER, ждущий блокировку, сокет не
	// читает и держал бы очередь читателей таблицы дальше дедлайна.
	var conns serverConns
	db := stdlib.OpenDB(*connCfg, stdlib.OptionAfterConnect(conns.add))
	finished := make(chan struct{})
	defer close(finished)
	stopCancel := context.AfterFunc(ctx, func() { conns.cancelUntil(finished, connCfg.ConnectTimeout) })
	defer stopCancel()

	dbDrv, err := migratepgx.WithInstance(db, &migratepgx.Config{MigrationsTable: src.VersionTable()})
	if err != nil {
		_ = db.Close()
		if ctx.Err() != nil {
			err = interruptedBy(ctx, err)
		}
		return fmt.Errorf("pgkit: migrate source %q: open: %w", src.Name, err)
	}
	m, err := migrate.NewWithInstance("iofs", srcDrv, "pgx5", dbDrv)
	if err != nil {
		_ = dbDrv.Close()
		return fmt.Errorf("pgkit: migrate source %q: %w", src.Name, err)
	}
	defer m.Close()

	err = fn(m)
	if ctx.Err() != nil {
		err = interruptedBy(ctx, err)
	}
	if err != nil {
		return fmt.Errorf("pgkit: migrate source %q: %w", src.Name, err)
	}
	return nil
}

// interruptedBy добавляет к ошибке прогона причину из ctx: errors.Is видит дедлайн.
func interruptedBy(ctx context.Context, err error) error {
	if err == nil {
		return fmt.Errorf("interrupted: %w", ctx.Err())
	}
	return fmt.Errorf("%w: %w", ctx.Err(), err)
}

// serverConns — серверные сессии соединений мигратора: на дедлайне им уходят cancel request'ы.
type serverConns struct {
	mu    sync.Mutex
	conns []*pgconn.PgConn
}

func (c *serverConns) add(_ context.Context, conn *pgx.Conn) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conns = append(c.conns, conn.PgConn())
	return nil
}

// cancelRepeat — пауза между cancel request'ами: оператор, начатый после дедлайна (между
// миграциями, когда прерывать было нечего), живёт не дольше неё.
const cancelRepeat = 100 * time.Millisecond

// cancelUntil шлёт cancel request каждой сессии, пока прогон не закончился; сессия без
// оператора его игнорирует. GracefulStop golang-migrate не годится: его флаг читают две
// горутины без синхронизации, и остановка между миграциями теряется.
func (c *serverConns) cancelUntil(finished <-chan struct{}, timeout time.Duration) {
	tick := time.NewTicker(cancelRepeat)
	defer tick.Stop()
	for {
		c.cancel(timeout)
		select {
		case <-finished:
			return
		case <-tick.C:
		}
	}
}

func (c *serverConns) cancel(timeout time.Duration) {
	c.mu.Lock()
	conns := slices.Clone(c.conns)
	c.mu.Unlock()
	for _, conn := range conns {
		ctx, stop := withOptionalTimeout(context.Background(), timeout)
		_ = conn.CancelRequest(ctx)
		stop()
	}
}

// Дедлайн ctx становится таймаутом коннекта: сам migrate открывает соединение
// на context.Background().
func migrateConnConfig(ctx context.Context, dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgkit: migrate: parse dsn: %w", err)
	}
	// Файлы миграций многооператорные — их принимает только simple protocol.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("pgkit: migrate: %w", ctx.Err())
		}
		cfg.ConnectTimeout = remaining
	}
	return cfg, nil
}
