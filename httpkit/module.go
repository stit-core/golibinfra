package httpkit

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// SecuritySchemeGroup — fx-группа схем безопасности OpenAPI (SecuritySchemeEntry).
const SecuritySchemeGroup = "httpkit.security_scheme"

// ProvideSecurityScheme регистрирует конструктор SecuritySchemeEntry (или []SecuritySchemeEntry —
// группа собирается через flatten) в группу SecuritySchemeGroup. Конструктор неверного типа
// отвергается при сборке графа: иначе схема молча не попадает в OpenAPI. Дополнительные
// аннотации применяются до групповой; свой fx.ResultTags передавать нельзя.
func ProvideSecurityScheme(ctor any, anns ...fx.Annotation) fx.Option {
	tag, err := securitySchemeTag(ctor)
	if err != nil {
		return fx.Error(err)
	}
	return fx.Provide(fx.Annotate(ctor, append(anns, fx.ResultTags(tag))...))
}

var (
	schemeType  = reflect.TypeFor[SecuritySchemeEntry]()
	schemesType = reflect.TypeFor[[]SecuritySchemeEntry]()
)

func securitySchemeTag(ctor any) (string, error) {
	t := reflect.TypeOf(ctor)
	if t == nil || t.Kind() != reflect.Func {
		return "", fmt.Errorf("httpkit: ProvideSecurityScheme expects a constructor function returning httpkit.SecuritySchemeEntry or []httpkit.SecuritySchemeEntry, got %T", ctor)
	}
	if t.NumOut() > 0 {
		switch t.Out(0) {
		case schemeType:
			return `group:"` + SecuritySchemeGroup + `"`, nil
		case schemesType:
			return `group:"` + SecuritySchemeGroup + `,flatten"`, nil
		}
	}
	return "", fmt.Errorf("httpkit: ProvideSecurityScheme constructor must return httpkit.SecuritySchemeEntry or []httpkit.SecuritySchemeEntry as its first result, got %v", t)
}

// Module — сервер, huma.API и /health + /ready; Config регистрирует caller
// (`fx.Provide(httpkit.NewConfig)` или из App-структуры). HTTP слушает после
// OnStart всех fx.Module графа и гасится до их OnStop: хук сервера вешает Invoke
// уровня Module, а fx выполняет его после Invoke всех fx.Module. Поэтому Module —
// на верхнем уровне fx.New, а корневой fx.Invoke, который строит зависимости (в
// том числе регистрирует роуты с хендлерами), — до него или внутри fx.Module.
var Module = fx.Options(
	fx.Module(
		"httpkit",
		fx.Provide(
			newServer,
			func(s *Server) huma.API { return s.API() },
		),
		fx.Invoke(registerHealth),
	),
	fx.Invoke(serve),
)

// Middlewares — net/http-middleware потребителя в порядке слайса (первый — внешний)
// внутри цепочки кита. Одно значение на граф: порядок задаёт приложение.
type Middlewares []func(http.Handler) http.Handler

// HumaConfigs — правки huma.Config потребителя в порядке слайса: после правок кита, но
// до хука описания ошибок, который видит их Security. Одно значение на граф: порядок
// задаёт приложение.
type HumaConfigs []func(*huma.Config)

// NewConfig читает HTTP_* из ENV и валидирует конфиг: невалидный — ошибка старта.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

type serverIn struct {
	fx.In

	Config Config
	Log    logkit.Logger

	Extract      AuthExtractor         `optional:"true"`
	Store        IdempotencyStore      `optional:"true"`
	Middlewares  Middlewares           `optional:"true"`
	HumaConfigs  HumaConfigs           `optional:"true"`
	PublicRoutes PublicRoutes          `optional:"true"`
	Schemes      []SecuritySchemeEntry `group:"httpkit.security_scheme"`
}

// healthIn — fx.In, а не fx.Annotate: ошибка графа (нет Logger, нет Config) называет функции
// кита и *httpkit.Server, а не reflect.makeFuncStub.
type healthIn struct {
	fx.In

	Server *Server
	Probes []Probe `group:"healthkit.probes"`
}

// registerHealth отвергает две пробы с одним именем: /ready гонял бы обе, а в логе
// readiness probe failed их не различить.
func registerHealth(in healthIn) error {
	names := make(map[string]bool, len(in.Probes))
	for _, p := range in.Probes {
		if names[p.Name] {
			return fmt.Errorf("%w: readiness probe %q is provided twice", ErrConfig, p.Name)
		}
		names[p.Name] = true
	}
	s := in.Server
	RegisterHealth(s.API(), s.cfg.ReadyProbeTimeout, s.log, in.Probes...)
	return nil
}

// newServer — NewServer из графа: схемы и конфиг проверяются уже в fx.New, а не на OnStart.
func newServer(in serverIn) (*Server, error) {
	opts := make([]ServerOption, 0, 5+len(in.HumaConfigs))
	opts = append(opts,
		WithSecuritySchemes(in.Schemes...),
		WithAuthExtractor(in.Extract),
		WithIdempotencyStore(in.Store),
		WithMiddleware(in.Middlewares...),
		WithPublicRoutes(in.PublicRoutes...),
	)
	for _, fn := range in.HumaConfigs {
		opts = append(opts, WithHumaConfig(fn))
	}
	s, err := NewServer(in.Config, in.Log, opts...)
	if err != nil {
		return nil, err
	}
	s.BindAPI()
	return s, nil
}

type serveIn struct {
	fx.In

	Lifecycle  fx.Lifecycle
	Shutdowner fx.Shutdowner
	Server     *Server
	Extract    AuthExtractor `optional:"true"`
}

// serve вешает хук сервера. Это Invoke уровня Module, а не конструктор: хуки идут
// в порядке добавления, и сервер обязан быть последним, чей хук добавлен.
func serve(in serveIn) {
	s, sd, log := in.Server, in.Shutdowner, in.Server.log
	in.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			// На OnStart все Invoke уже выполнены: операции зарегистрированы целиком.
			if err := s.CheckRoutes(); err != nil {
				return err
			}
			if ops := securedOperations(s.registry()); in.Extract == nil && len(ops) > 0 {
				return fmt.Errorf("%w: operations with Security need an httpkit.AuthExtractor in the graph: %s",
					ErrAuthNotConfigured, strings.Join(ops, ", "))
			}
			if err := s.Listen(); err != nil {
				return err
			}
			log.Info(ctx, "http server listening", "addr", s.Addr().String())
			go func() { //nolint:gosec // горутина сервера живёт дольше ctx OnStart; фоновый ctx только для терминального лога
				if err := s.Serve(); err != nil {
					log.Error(context.Background(), "http server stopped", "err", err)
					// Упавший listener — не «работающий» сервис: fx должен погасить приложение.
					_ = sd.Shutdown(fx.ExitCode(1))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			// Треть остатка бюджета остановки — хукам после HTTP (подписки, пул, телеметрия): долгий
			// запрос или поток её не съедает.
			if deadline, ok := ctx.Deadline(); ok {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Until(deadline)*2/3)
				defer cancel()
			}
			return s.Stop(ctx)
		},
	})
}

// securedOperations — операции с Security, в том числе скрытые, по имени.
func securedOperations(reg *registry) []string {
	var names []string
	for _, op := range reg.ops {
		if len(op.Security) > 0 {
			names = append(names, operationName(op))
		}
	}
	slices.Sort(names)
	return names
}
