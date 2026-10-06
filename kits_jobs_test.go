package golibinfra_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	sessionpgstore "git.stit.tech/stit-core/golibinfra/authnkit/session/pgstore"
	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	bruteforcepgstore "git.stit.tech/stit-core/golibinfra/bruteforcekit/pgstore"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	outboxpgstore "git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit/natstest"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Регрессия KX61b: job'ы китов, которым нужна схема, стартуют после миграций при любом
// порядке модулей — хук timerkit встаёт в очередь OnStart за хуком pgkit.MigrateModule,
// потому что job зависит от optional pgkit.Migrated. Миграции падают на недоступной БД:
// job, стартовавший раньше, виден по событию хука.
func TestKitSchemaJobsStartAfterMigrations(t *testing.T) {
	cases := map[string]fx.Option{
		"bruteforcekit/pgstore":    fx.Options(fx.Provide(bruteforcekit.NewConfig), bruteforcepgstore.Module),
		"authnkit/session/pgstore": fx.Options(fx.Provide(session.NewConfig), sessionpgstore.Module),
		"outboxkit": fx.Options(
			fx.Provide(
				outboxkit.NewConfig,
				func() outboxkit.Repository { return idleRepo{} },
				func() natskit.StreamBus { return natstest.New() },
				func() pgkit.Transactor { return pgtest.Identity{} },
			),
			outboxkit.Module,
		),
		"outboxkit.Instance": fx.Options(
			fx.Provide(
				outboxkit.NewConfig,
				func() outboxkit.RepositoryFactory { return idleFactory{} },
				func() natskit.StreamBus { return natstest.New() },
				func() pgkit.Transactor { return pgtest.Identity{} },
			),
			outboxkit.Instance("orders", "outbox_orders"),
		),
		"outboxkit/pgstore": fx.Options(fx.Provide(outboxpgstore.NewConfig), outboxpgstore.Module),
	}
	for name, kit := range cases {
		t.Run(name, func(t *testing.T) {
			hooks := &hookOrder{}
			app := fx.New(
				fx.WithLogger(func() fxevent.Logger { return hooks }),
				fx.Provide(logkit.Nop),
				timerkit.Module, // раньше миграций: порядок держит pgkit.Migrated, а не позиция
				kit,
				fx.Supply(
					pgkit.NewDB(nil),
					pgkit.PoolConfig{DSN: "postgres://u@127.0.0.1:1/d?connect_timeout=1"},
					pgkit.MigrateRunnerConfig{Timeout: 5 * time.Second},
				),
				pgkit.SupplyMigrations(pgkit.Source{Name: "zz", FS: fstest.MapFS{
					"m/0001_zz.up.sql": {Data: []byte("SELECT 1;")},
				}, Dir: "m"}),
				pgkit.MigrateModule,
			)
			if err := app.Err(); err != nil {
				t.Fatalf("fx.New: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := app.Start(ctx); err == nil {
				t.Fatal("Start = nil, want the migration error of an unreachable database")
			}
			_ = app.Stop(ctx)
			migrate, jobs := hooks.index("pgkit.provideMigrated"), hooks.index("timerkit.startJobs")
			if migrate < 0 {
				t.Fatalf("migration hook did not run: %v", hooks.callers)
			}
			if jobs >= 0 && jobs < migrate {
				t.Fatalf("timerkit started jobs before migrations: %v", hooks.callers)
			}
		})
	}
}

// hookOrder — вызывающие OnStart-хуков в порядке исполнения.
type hookOrder struct{ callers []string }

func (h *hookOrder) LogEvent(e fxevent.Event) {
	if ev, ok := e.(*fxevent.OnStartExecuting); ok {
		h.callers = append(h.callers, ev.CallerName)
	}
}

func (h *hookOrder) index(caller string) int {
	for i, c := range h.callers {
		if strings.HasSuffix(c, "/"+caller) {
			return i
		}
	}
	return -1
}

// idleFactory — хранилища инстансов без строк и без job'ов обслуживания.
type idleFactory struct{}

func (idleFactory) Repository(string) (outboxkit.Repository, error) { return idleRepo{}, nil }

func (idleFactory) Migrations(table string) pgkit.Source {
	return pgkit.Source{Name: table, FS: fstest.MapFS{"m/0001_t.up.sql": {Data: []byte("SELECT 1;")}}, Dir: "m"}
}

// idleRepo — Repository без строк: drain-job до тика к нему не обращается.
type idleRepo struct{}

func (idleRepo) Insert(context.Context, string, json.RawMessage) error { return nil }

func (idleRepo) ListUnpublished(context.Context, int, outboxkit.PublishMode) ([]outboxkit.Message, error) {
	return nil, nil
}

func (idleRepo) MarkPublished(context.Context, []outboxkit.MessageRef) error { return nil }

func (idleRepo) RecordFailures(context.Context, []outboxkit.Failure) error { return nil }
