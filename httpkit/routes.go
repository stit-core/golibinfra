package httpkit

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
)

// ErrRoutes — операции и маршруты сервера не прошли проверку старта (CheckRoutes).
var ErrRoutes = errors.New("httpkit: invalid routes")

// publicKey — ключ Operation.Metadata, которым Public помечает операцию.
const publicKey = "httpkit.public"

// Public помечает операцию открытой без аутентификации: при схеме безопасности в графе
// операция без Security и без этой пометки — ошибка старта. Пустой Security публичным не
// считается.
func Public(op huma.Operation) huma.Operation {
	op.Metadata = maps.Clone(op.Metadata)
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[publicKey] = true
	return op
}

func isPublic(op *huma.Operation) bool {
	public, _ := op.Metadata[publicKey].(bool)
	return public
}

// PublicRoutes — пути (паттерны chi) маршрутов мимо huma.Register — Router() и
// Adapter().Handle, — открытых без аутентификации; одно значение на граф. При схеме
// безопасности в графе другой такой маршрут — ошибка старта.
type PublicRoutes []string

// registry — huma.API сервера: huma.Register отдаёт ему каждую операцию, в том числе
// скрытую и из группы huma (OperationDocumenter), а OnAddOperation скрытых не видит.
type registry struct {
	huma.API
	adapter *opAdapter
	ops     []*huma.Operation
	// pending — операции, которым huma.Register ещё не поставил хендлер.
	pending []*huma.Operation
	// replaced — маршруты chi, которые операция заменила при регистрации.
	replaced []string
}

// DocumentOperation запоминает операцию и добавляет в OpenAPI нескрытую — как huma без
// OperationDocumenter.
func (r *registry) DocumentOperation(op *huma.Operation) {
	r.ops = append(r.ops, op)
	r.pending = append(r.pending, op)
	if !op.Hidden {
		r.OpenAPI().AddOperation(op)
	}
}

// Adapter — адаптер реестра: его зовут huma.Register и группы huma.
func (r *registry) Adapter() huma.Adapter { return r.adapter }

// claim — операция без хендлера с тем же METHOD и путём: huma.Register документирует её до
// Handle, группа — копией с префиксом.
func (r *registry) claim(op *huma.Operation) *huma.Operation {
	i := slices.IndexFunc(r.pending, func(p *huma.Operation) bool { return p.Method == op.Method && p.Path == op.Path })
	if i < 0 {
		return nil
	}
	claimed := r.pending[i]
	r.pending = slices.Delete(r.pending, i, i+1)
	return claimed
}

// opHandler — хендлер маршрута операции из реестра: по типу CheckRoutes отличает его от
// маршрута chi, который на том же METHOD+path молча заменил операцию.
type opHandler struct{ http.HandlerFunc }

// opAdapter — адаптер humachi, который ставит операциям реестра хендлер opHandler; прочее
// (Adapter().Handle мимо huma.Register) — обычный маршрут chi.
type opAdapter struct {
	huma.Adapter
	router chi.Router
	reg    *registry
}

// Handle регистрирует хендлер как humachi; маршрут, который операция заменяет, — в replaced.
func (a *opAdapter) Handle(op *huma.Operation, handler func(huma.Context)) {
	serve := func(w http.ResponseWriter, r *http.Request) { handler(humachi.NewContext(op, r, w)) }
	documented := a.reg.claim(op)
	if documented == nil {
		a.router.MethodFunc(op.Method, op.Path, serve)
		return
	}
	route := op.Method + " " + routeKey(op.Path)
	if h, ok := routeHandlers(a.router)[route]; ok {
		if _, isOp := h.(opHandler); !isOp {
			a.reg.replaced = append(a.reg.replaced, fmt.Sprintf("route %s is registered before operation %s, which replaces it", route, operationName(documented)))
		}
	}
	a.router.Method(op.Method, op.Path, opHandler{serve})
}

// SanitizeInternalContext — опциональный интерфейс адаптера humachi (autopatch huma).
func (a *opAdapter) SanitizeInternalContext(ctx context.Context) context.Context {
	if s, ok := a.Adapter.(interface {
		SanitizeInternalContext(context.Context) context.Context
	}); ok {
		return s.SanitizeInternalContext(ctx)
	}
	return ctx
}

// Config — huma.Config API: его читают huma.Register и группы huma.
func (r *registry) Config() huma.Config {
	if c, ok := r.API.(interface{ Config() huma.Config }); ok {
		return c.Config()
	}
	return huma.Config{}
}

// CheckRoutes — проверка старта: METHOD+path у операций не повторяется и не занят маршрутом
// chi мимо huma.Register (в любом порядке регистрации), Public — без Security. Если в
// components.securitySchemes есть схема, у каждой операции, в том числе скрытой, — Security
// из components или Public, а маршрут chi мимо huma.Register (кроме /docs, /openapi.*,
// /schemas) — в PublicRoutes. Module зовёт её на OnStart, после всех Invoke; маршрут,
// добавленный позже, она не видит. Ошибка — ErrRoutes с именами операций и маршрутов.
func (s *Server) CheckRoutes() error {
	reg := s.registry()
	schemes := reg.OpenAPI().Components.SecuritySchemes
	guarded := len(schemes) > 0

	var problems []string
	ops := make(map[string]string, len(reg.ops))
	for _, op := range reg.ops {
		name := operationName(op)
		route := op.Method + " " + routeKey(op.Path)
		if prev, dup := ops[route]; dup {
			problems = append(problems, fmt.Sprintf("operations %s and %s share %s", prev, name, route))
		}
		ops[route] = name
		switch public := isPublic(op); {
		case public && len(op.Security) > 0:
			problems = append(problems, fmt.Sprintf("operation %s is httpkit.Public but has Security", name))
		case guarded && !public && len(op.Security) == 0:
			problems = append(problems, fmt.Sprintf("operation %s has no Security and is not httpkit.Public", name))
		}
		for _, req := range op.Security {
			for scheme := range req {
				if _, ok := schemes[scheme]; guarded && !ok {
					problems = append(problems, fmt.Sprintf("operation %s requires security scheme %q missing from components", name, scheme))
				}
			}
		}
	}
	problems = append(problems, reg.replaced...)
	problems = append(problems, s.rawRouteProblems(ops, guarded)...)
	if len(problems) == 0 {
		return nil
	}
	slices.Sort(problems)
	return fmt.Errorf("%w: %s", ErrRoutes, strings.Join(slices.Compact(problems), "; "))
}

// rawRouteProblems — маршруты chi мимо huma.Register: на пути операции (chi молча оставил
// последний хендлер — middleware huma там не действует), а при схеме (guarded) — и вне
// документации huma и PublicRoutes; плюс строки PublicRoutes без такого маршрута.
func (s *Server) rawRouteProblems(ops map[string]string, guarded bool) []string {
	public := make(map[string]bool, len(s.opts.publicRoutes))
	for _, p := range s.opts.publicRoutes {
		public[routeKey(p)] = false
	}
	var problems []string
	for route, h := range routeHandlers(s.router) {
		if _, isOp := h.(opHandler); isOp {
			continue
		}
		if name, registered := ops[route]; registered {
			problems = append(problems, fmt.Sprintf("route %s replaces operation %s", route, name))
			continue
		}
		if !guarded || s.docsRoutes[route] {
			continue
		}
		path := route[strings.IndexByte(route, ' ')+1:]
		if _, declared := public[path]; declared {
			public[path] = true
			continue
		}
		problems = append(problems, fmt.Sprintf("route %s bypasses huma.Register: register an operation or list the path in httpkit.PublicRoutes", route))
	}
	for path, used := range public {
		if guarded && !used {
			problems = append(problems, fmt.Sprintf("httpkit.PublicRoutes lists %s, but no route bypasses huma.Register there", path))
		}
	}
	return problems
}

// checkSchemes отвергает схему без имени или без Scheme и два значения с одним именем: из
// группы fx значения приходят в случайном порядке, и в OpenAPI попала бы случайная схема.
func checkSchemes(entries []SecuritySchemeEntry) error {
	seen := make(map[string]*huma.SecurityScheme, len(entries))
	for _, e := range entries {
		if e.Name == "" || e.Scheme == nil {
			return fmt.Errorf("%w: security scheme %q needs a Name and a Scheme", ErrConfig, e.Name)
		}
		if prev, dup := seen[e.Name]; dup {
			return fmt.Errorf("%w: security scheme %q is provided twice: %s and %s",
				ErrConfig, e.Name, describeScheme(prev), describeScheme(e.Scheme))
		}
		seen[e.Name] = e.Scheme
	}
	return nil
}

func describeScheme(s *huma.SecurityScheme) string {
	return strings.Join(strings.Fields(strings.Join([]string{s.Type, s.Scheme, s.In, s.Name}, " ")), " ")
}

var pathParam = regexp.MustCompile(`\{[^}]*\}`)

// routeKey — путь без имён параметров: chi считает /users/{id} и /users/{userID} одним
// маршрутом, и второй молча заменяет первый.
func routeKey(path string) string {
	return pathParam.ReplaceAllString(path, "{}")
}

func operationName(op *huma.Operation) string {
	if op.OperationID != "" {
		return op.OperationID
	}
	return op.Method + " " + op.Path
}

// routeHandlers — хендлеры маршрутов роутера ключами «METHOD путь» (routeKey).
func routeHandlers(r chi.Routes) map[string]http.Handler {
	routes := map[string]http.Handler{}
	_ = chi.Walk(r, func(method, pattern string, h http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+routeKey(pattern)] = h
		return nil
	})
	return routes
}
