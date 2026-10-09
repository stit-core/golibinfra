package golibinfra_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	fxlib "go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/config"
	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// docFx — имя `fx` внутри сниппета доков: New проверяет граф (fx.ValidateApp) вместо сборки и
// запуска, остальное — сам fx.
type docFx struct{ err *error }

func (d docFx) New(opts ...fxlib.Option) docApp {
	*d.err = fxlib.ValidateApp(opts...)
	return docApp{}
}

func (docFx) Provide(ctors ...any) fxlib.Option { return fxlib.Provide(ctors...) }

func (docFx) Invoke(funcs ...any) fxlib.Option { return fxlib.Invoke(funcs...) }

func (docFx) Annotate(target any, anns ...fxlib.Annotation) any {
	return fxlib.Annotate(target, anns...)
}

func (docFx) ParamTags(tags ...string) fxlib.Annotation { return fxlib.ParamTags(tags...) }

type docApp struct{}

func (docApp) Run() {}

// Типы потребителя, которые называют сниппеты.
type (
	GetUserInput struct {
		ID string `path:"id"`
	}
	GetUserOutput struct{ Body string }
	userService   struct{}
	appModule     struct{ Module fxlib.Option }
	appConfig     struct {
		Postgres pgkit.PoolConfig
		HTTP     httpkit.Config
	}
)

var ErrUserNotFound = errors.New("user not found")

func (userService) Get(context.Context, string) (string, error) { return "", ErrUserNotFound }

// docHTTPKitQuickStart — httpkit/README.md § Быстрый старт.
func docHTTPKitQuickStart(fx docFx, svc userService) {
	userErrors := httpkit.NewErrorMap(
		httpkit.ErrorRule{Err: ErrUserNotFound, Status: http.StatusNotFound, Code: "USER_NOT_FOUND", Detail: "user not found"},
	)

	app := fx.New(
		logkit.Module, // logkit.Logger
		fx.Provide(logkit.NewConfig, logkit.NewRedactConfig, httpkit.NewConfig, pgkit.NewPoolConfig), // LOG_*, HTTP_* и PG_* из ENV
		pgkit.Module,                        // *pgkit.DB — её берёт проба
		healthkit.ProvideProbe(pgkit.Probe), // проба в /ready
		fx.Invoke(func(api huma.API) { // корневой Invoke — до httpkit.Module
			huma.Register(api, userErrors.Declare(huma.Operation{ // коды — в openapi.json
				OperationID: "getUser", Method: http.MethodGet, Path: "/users/{id}",
			}), func(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
				u, err := svc.Get(ctx, in.ID)
				if err != nil {
					return nil, userErrors.Map(err) // несопоставленная → 500 без причины + лог
				}
				return &GetUserOutput{Body: u}, nil
			})
		}),
		httpkit.Module, // *Server + huma.API; HTTP стартует последним
	)
	app.Run()
}

// docConfigQuickStart — config/README.md § Быстрый старт, граф приложения.
func docConfigQuickStart(fx docFx, appconfig appModule) {
	fx.New(
		config.Module, // env → []config.Source
		appconfig.Module,
		fx.Provide(logkit.NewConfig), // конфиг кита вне App — его конструктором
		logkit.Module,
		pgkit.Module,
		httpkit.Module,
	)
}

// docPgkitProbes — pgkit/docs/usage.md § Health-пробы.
func docPgkitProbes(fx docFx) {
	fx.New(
		logkit.Module, pgkit.Module, httpkit.Module,
		fx.Provide(logkit.NewConfig, pgkit.NewPoolConfig, httpkit.NewConfig),

		healthkit.ProvideProbe(pgkit.Probe),        // /ready → 503, если SELECT 1 не проходит
		healthkit.ProvideProbe(pgkit.ReplicaProbe), // без PG_REPLICA_DSN проходит без запроса
	)
}

// docOutboxInstances — outboxkit/docs/usage.md § Несколько инстансов.
func docOutboxInstances(fx docFx, orderedCfg outboxkit.Config) {
	fx.New(
		fx.Provide(logkit.NewConfig, pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig, natskit.NewConfig,
			pgstore.NewConfig, outboxkit.NewConfig),
		logkit.Module, pgkit.Module, pgkit.MigrateModule, natskit.Module(""), timerkit.Module,

		pgstore.FactoryModule, // outboxkit.RepositoryFactory
		outboxkit.Instance("orders", "outbox_orders", outboxkit.WithSubjects("orders.>")),
		outboxkit.Instance("leads", "outbox_leads", outboxkit.WithSubjects("leads.>"),
			outboxkit.WithInstanceConfig(orderedCfg)), // свой режим у одного домена

		fx.Invoke(fx.Annotate(
			func(orders outboxkit.Publisher) { /* в доменный сервис */ },
			fx.ParamTags(`name:"orders"`),
		)),
	)
}

// Регрессия RDOC-01: полный граф `fx.New` в доках собирается как написан. Блок доки — тело
// функции этого файла байт в байт (отступ доки — четыре пробела), поэтому сниппет правится только
// вместе с кодом, а код проходит fx.ValidateApp: пропущенный провайдер — отказ гейта, а не первого
// запуска у потребителя.
func TestDocGraphs(t *testing.T) {
	source, err := os.ReadFile("doc_graphs_test.go")
	if err != nil {
		t.Fatal(err)
	}
	ordered := outboxkit.Config{
		BatchSize: 1, Interval: 1, PublishMode: outboxkit.PublishOrdered, TickTimeout: 1, MaxAttempts: 1, MaxPayloadBytes: 1,
	}
	app := appModule{Module: fxlib.Module("appconfig", fxlib.Provide(
		func([]config.Source) (*appConfig, error) { return &appConfig{}, nil },
		func(a *appConfig) pgkit.PoolConfig { return a.Postgres },
		func(a *appConfig) httpkit.Config { return a.HTTP },
	))}
	for _, g := range []struct {
		fn, doc string
		run     func(fx docFx)
	}{
		{"docHTTPKitQuickStart", "httpkit/README.md", func(fx docFx) { docHTTPKitQuickStart(fx, userService{}) }},
		{"docConfigQuickStart", "config/README.md", func(fx docFx) { docConfigQuickStart(fx, app) }},
		{"docPgkitProbes", "pgkit/docs/usage.md", docPgkitProbes},
		{"docOutboxInstances", "outboxkit/docs/usage.md", func(fx docFx) { docOutboxInstances(fx, ordered) }},
	} {
		t.Run(g.fn, func(t *testing.T) {
			_, rest, _ := strings.Cut(string(source), "\nfunc "+g.fn+"(")
			_, rest, _ = strings.Cut(rest, ") {\n")
			body, _, ok := strings.Cut(rest, "\n}\n")
			snippet := strings.ReplaceAll(strings.ReplaceAll("\n"+body, "\n\t", "\n"), "\t", "    ")[1:] + "\n"
			doc, err := os.ReadFile(g.doc)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || !strings.Contains(string(doc), "```go\n"+snippet+"```\n") {
				t.Fatalf("%s: the fx.New block is not the body of %s; edit both:\n%s", g.doc, g.fn, snippet)
			}
			var graphErr error
			g.run(docFx{err: &graphErr})
			if graphErr != nil {
				t.Fatalf("%s: the graph does not build as written: %v", g.doc, graphErr)
			}
		})
	}
}
