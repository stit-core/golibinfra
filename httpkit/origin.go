package httpkit

import (
	"fmt"
	"maps"
	"mime"
	"net/http"
	"net/textproto"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// SessionCookies — имена cookie сессии сервиса: изменяющий запрос (любой метод, кроме GET, HEAD, OPTIONS и TRACE) с
// любой из них проходит только с Origin, точно совпавшим с записью HTTP_CORS_ORIGINS («*» и «null» адресов не
// разрешают), иначе — 403 ORIGIN_NOT_ALLOWED до тела, HumaAuth и хендлера; этот 403 — в OpenAPI каждой операции
// изменяющего метода. Одно значение на граф; без адреса в HTTP_CORS_ORIGINS Module не стартует (ErrConfig).
type SessionCookies []string

// issuesSessionKey — ключ Operation.Metadata, которым IssuesSession помечает операцию.
const issuesSessionKey = "httpkit.issuesSession"

// IssuesSession помечает операцию, которая выдаёт сессию запросу без неё (вход): при SessionCookies изменяющий запрос к
// ней с Origin, которого нет в HTTP_CORS_ORIGINS (и с «null»), — 403 ORIGIN_NOT_ALLOWED и без cookie сессии: форма
// страницы соседнего адреса того же сайта иначе вошла бы в браузере пользователя под чужой учётной записью (подделка
// входа). Без Origin и без cookie сессии запрос проходит, только если такое тело браузер без preflight не шлёт:
// Content-Type корректен и не text/plain и не форма (JSON клиента вне браузера); пустой и битый — отказ. Метка у
// операции безопасного метода — ошибка CheckRoutes.
func IssuesSession(op huma.Operation) huma.Operation {
	op.Metadata = maps.Clone(op.Metadata)
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[issuesSessionKey] = true
	return op
}

func issuesSession(op *huma.Operation) bool {
	issues, _ := op.Metadata[issuesSessionKey].(bool)
	return issues
}

// codeOriginNotAllowed — code 403 проверки Origin: его же при cookie сессии объявляет в OpenAPI каждая операция
// изменяющего метода.
const codeOriginNotAllowed = "ORIGIN_NOT_ALLOWED"

// maxLoggedHeader — потолок заголовка клиента (Origin, Content-Type) в записи лога.
const maxLoggedHeader = 256

// checkSessionCookies отвергает имя, которого не бывает у cookie: запрос его не принёс бы, и проверка Origin молча не
// действовала бы.
func checkSessionCookies(names []string) error {
	for _, name := range names {
		if err := (&http.Cookie{Name: name}).Valid(); err != nil { //nolint:gosec // cookie проверяет только имя: в ответ она не уходит
			return fmt.Errorf("%w: session cookie %q is not a valid cookie name", ErrConfig, name)
		}
	}
	return nil
}

// cookieOrigins — адреса, с которых меняет данные запрос с cookie сессии: записи cors.Origins без пробелов вокруг,
// кроме «*» и «null».
func cookieOrigins(cors CORSConfig) map[string]bool {
	set := map[string]bool{}
	for _, o := range cors.Origins {
		if o = strings.TrimSpace(o); o != "" && o != "*" && o != "null" {
			set[o] = true
		}
	}
	return set
}

// safeMethod — метод, который данных не меняет (RFC 9110 §9.2.1): проверка Origin его не касается.
func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// originCheck — изменяющий запрос с cookie сессии (cookies) проходит только с Origin из cors.Origins: без него или с
// чужим — 403 ORIGIN_NOT_ALLOWED, WARN и http_origin_rejections_total. Без cookies — middleware нет.
func originCheck(cors CORSConfig, cookies []string, log logkit.Logger) func(http.Handler) http.Handler {
	if len(cookies) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	allowed := cookieOrigins(cors)
	instr := newInstruments()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if safeMethod(r.Method) || allowed[origin] || !carriesCookie(r, cookies) {
				next.ServeHTTP(w, r)
				return
			}
			reason := "not_allowed"
			if origin == "" {
				reason = "missing"
			}
			instr.originRejections.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("path", routeLabel(r)),
				attribute.String("reason", reason),
			))
			log.Warn(r.Context(), "session cookie refused: origin is not allowed",
				"method", r.Method, "origin", truncate(origin, maxLoggedHeader))
			writeAPIError(r.Context(), w, Forbidden(codeOriginNotAllowed, textOriginNotAllowed))
		})
	}
}

// issuerCheck — хендлер операции IssuesSession (op) при cookie сессии (cookies): запрос с Origin не из cors.Origins и
// запрос без Origin с телом, которое браузер шлёт без preflight, — 403 ORIGIN_NOT_ALLOWED, WARN и
// http_origin_rejections_total и без cookie. Стоит на маршруте операции, а не в цепочке: путь, который middleware
// потребителя переписал до маршрутизации, его не обходит.
func issuerCheck(cors CORSConfig, cookies []string, log logkit.Logger, op *huma.Operation, serve http.HandlerFunc) http.HandlerFunc {
	if len(cookies) == 0 || !issuesSession(op) {
		return serve
	}
	allowed := cookieOrigins(cors)
	instr := newInstruments()
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowed[origin] || origin == "" && !simpleBody(r) {
			serve(w, r)
			return
		}
		reason := "not_allowed"
		if origin == "" {
			reason = "missing"
		}
		instr.originRejections.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("path", routeLabel(r)),
			attribute.String("reason", reason),
		))
		log.Warn(r.Context(), "session issuer refused: origin is not allowed", "method", r.Method,
			"origin", truncate(origin, maxLoggedHeader), "content_type", truncate(r.Header.Get("Content-Type"), maxLoggedHeader))
		writeAPIError(r.Context(), w, Forbidden(codeOriginNotAllowed, textOriginNotAllowed))
	}
}

// simpleBody — тело, которое браузер может прислать на чужой адрес без preflight (Fetch, CORS-safelisted
// Content-Type): Content-Type нет, он битый или он text/plain, application/x-www-form-urlencoded или
// multipart/form-data. Разбор строгий: Firefox считает простым и «text/plain +json;», а huma читает такое тело как JSON.
// Без Origin такое тело шлёт и браузер (Firefox до 70 и Edge до 79 не ставят Origin обычному POST).
func simpleBody(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return true
	}
	switch mediaType {
	case "text/plain", "application/x-www-form-urlencoded", "multipart/form-data":
		return true
	}
	return false
}

// carriesCookie — в запросе есть cookie с одним из имён, хоть и пустая. Имя ищется в каждой части каждой строки Cookie
// без предела их числа: r.Cookie при частях сверх GODEBUG httpcookiemaxnum не видит ни одной, а вход huma
// `cookie:"…"` их читает, — поиск не уже ни одного из тех, кто cookie читает.
func carriesCookie(r *http.Request, names []string) bool {
	for _, line := range r.Header.Values("Cookie") {
		for part := range strings.SplitSeq(line, ";") {
			name, _, _ := strings.Cut(part, "=")
			if slices.Contains(names, textproto.TrimString(name)) {
				return true
			}
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// checkSessionOrigins — проверка старта Module: без адреса в HTTP_CORS_ORIGINS каждый изменяющий запрос с cookie
// сессии получал бы 403. Граф без старта (спецификация, проверка маршрутов) собирается и без адреса.
func (s *Server) checkSessionOrigins() error {
	if len(s.opts.sessionCookies) == 0 || len(cookieOrigins(s.cfg.CORS)) > 0 {
		return nil
	}
	return fmt.Errorf("%w: session cookies %s change data only from an origin of HTTP_CORS_ORIGINS, and it has none (\"*\" and \"null\" are not origins)",
		ErrConfig, strings.Join(s.opts.sessionCookies, ", "))
}
