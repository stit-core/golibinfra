package httpkit

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
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
	ops []*huma.Operation
}

// DocumentOperation запоминает операцию и добавляет в OpenAPI нескрытую — как huma без
// OperationDocumenter.
func (r *registry) DocumentOperation(op *huma.Operation) {
	r.ops = append(r.ops, op)
	if !op.Hidden {
		r.OpenAPI().AddOperation(op)
	}
}

// Config — huma.Config API: его читают huma.Register и группы huma.
func (r *registry) Config() huma.Config {
	if c, ok := r.API.(interface{ Config() huma.Config }); ok {
		return c.Config()
	}
	return huma.Config{}
}

// CheckRoutes — проверка старта: METHOD+path у операций не повторяется, Public — без
// Security. Если в components.securitySchemes есть схема, у каждой операции, в том числе
// скрытой, — Security из components или Public, а маршрут chi мимо huma.Register (кроме
// /docs, /openapi.*, /schemas) — в PublicRoutes. Module зовёт её на OnStart, после всех
// Invoke; ошибка — ErrRoutes с именами операций и маршрутов, у невалидной схемы — ErrConfig.
func (s *Server) CheckRoutes() error {
	if err := checkSchemes(s.opts.schemes); err != nil {
		return err
	}
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
	if guarded {
		problems = append(problems, s.rawRouteProblems(ops)...)
	}
	if len(problems) == 0 {
		return nil
	}
	slices.Sort(problems)
	return fmt.Errorf("%w: %s", ErrRoutes, strings.Join(slices.Compact(problems), "; "))
}

// rawRouteProblems — маршруты chi вне реестра, документации huma и PublicRoutes, и строки
// PublicRoutes без такого маршрута: middleware huma (HumaAuth) на них не действует.
func (s *Server) rawRouteProblems(ops map[string]string) []string {
	public := make(map[string]bool, len(s.opts.publicRoutes))
	for _, p := range s.opts.publicRoutes {
		public[routeKey(p)] = false
	}
	var problems []string
	_ = chi.Walk(s.router, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route := method + " " + routeKey(pattern)
		if _, registered := ops[route]; registered || s.docsRoutes[route] {
			return nil
		}
		if _, ok := public[routeKey(pattern)]; ok {
			public[routeKey(pattern)] = true
			return nil
		}
		problems = append(problems, fmt.Sprintf("route %s bypasses huma.Register: register an operation or list the path in httpkit.PublicRoutes", route))
		return nil
	})
	for path, used := range public {
		if !used {
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

// chiRoutes — маршруты роутера ключами «METHOD путь» (routeKey).
func chiRoutes(r chi.Routes) map[string]bool {
	routes := map[string]bool{}
	_ = chi.Walk(r, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+routeKey(pattern)] = true
		return nil
	})
	return routes
}
