package httpkit_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/danielgtaylor/huma/v2/sse"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

const (
	appOrigin   = "https://app.example.com"
	adminOrigin = "https://admin.example.com"
	// sessionValue — значение cookie сессии в запросах теста: в лог отказа оно не попадает.
	sessionValue = "s3cr3t-session"
)

// changingMethods — методы, которые меняют данные: изменяющему запросу с cookie сессии нужен Origin из списка;
// PROPFIND — метод, которого кит не знает, тоже с проверкой.
var changingMethods = []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, "PROPFIND"}

// safeMethods — методы, которые данных не меняют (RFC 9110 §9.2.1): Origin не сверяется.
var safeMethods = []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace}

// originCases — Origin изменяющего запроса и принят ли он при списке app и admin: точное совпадение с записью, как у
// CORS.
func originCases() map[string]bool {
	return map[string]bool{
		appOrigin:                      true,
		adminOrigin:                    true,
		"":                             false,
		"null":                         false,
		"https://evil.example.com":     false,
		"http://app.example.com":       false,
		"https://app.example.com:8443": false,
		appOrigin + "/":                false,
		"https://APP.example.com":      false,
	}
}

// cookieCases — заголовок Cookie запроса и есть ли в нём cookie сессии sid или refresh.
func cookieCases() map[string]bool {
	return map[string]bool{
		"sid=" + sessionValue:             true,
		"refresh=r1":                      true,
		"theme=dark; sid=" + sessionValue: true,
		"sid=":                            true,
		"theme=dark":                      false,
		"":                                false,
		"SID=" + sessionValue:             false,
		"sid_old=" + sessionValue:         false,
		"theme=dark; refresh_hint=r1":     false,
	}
}

func headers(cookie, origin string, more ...string) []any {
	var hs []any
	if cookie != "" {
		hs = append(hs, "Cookie: "+cookie)
	}
	if origin != "" {
		hs = append(hs, "Origin: "+origin)
	}
	for _, h := range more {
		hs = append(hs, h)
	}
	return hs
}

// cookieAPI — API сервера с cookie сессии sid и refresh поверх cfg: те же сборка и проверка маршрутов, что у Module.
func cookieAPI(t *testing.T, cfg httpkit.Config, register func(huma.API), opts ...httpkit.ServerOption) humatest.TestAPI {
	t.Helper()
	s := mustServer(t, cfg, append([]httpkit.ServerOption{httpkit.WithSessionCookies("sid", "refresh")}, opts...)...)
	api := s.BindAPI()
	register(api)
	if err := s.CheckRoutes(); err != nil {
		t.Fatal(err)
	}
	return humatest.Wrap(t, api)
}

// originConfig — конфиг по умолчанию с адресами origins; лимитер выключен: матрица шлёт сотни запросов с одного адреса.
func originConfig(t *testing.T, origins ...string) httpkit.Config {
	t.Helper()
	cfg := testConfig(t, "127.0.0.1:0")
	cfg.CORS.Origins = origins
	cfg.RateLimit.RPS = 0
	return cfg
}

// registerThings — операции /things всех методов huma: хендлер считает вызовы.
func registerThings(calls *atomic.Int32) func(huma.API) {
	return func(api huma.API) {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			huma.Register(api, httpkit.Public(huma.Operation{OperationID: "things-" + strings.ToLower(method), Method: method, Path: "/things"}),
				func(context.Context, *struct{}) (*struct{}, error) {
					calls.Add(1)
					return nil, nil // тело ответа не нужно
				})
		}
	}
}

// Матрица метод × cookie × Origin: изменяющий запрос с cookie сессии проходит только с Origin из HTTP_CORS_ORIGINS —
// без Origin, с null и с адресом другой схемы, порта, регистра или с хвостовым «/» — 403 ORIGIN_NOT_ALLOWED до
// хендлера; запрос без cookie сессии и безопасный метод Origin не сверяют.
func TestSessionCookies_Matrix(t *testing.T) {
	var calls atomic.Int32
	api := cookieAPI(t, originConfig(t, appOrigin, adminOrigin), registerThings(&calls))
	routed := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

	for _, method := range slices.Concat(changingMethods, safeMethods) {
		for cookie, carries := range cookieCases() {
			for origin, allowed := range originCases() {
				refused := !slices.Contains(safeMethods, method) && carries && !allowed
				before := calls.Load()
				resp := api.Do(method, "/things", headers(cookie, origin)...)
				switch {
				case refused:
					handlertest.AssertAPIError(t, resp, http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
					if calls.Load() != before {
						t.Errorf("%s with %q from %q: the handler ran after the refusal", method, cookie, origin)
					}
				case resp.Code == http.StatusForbidden:
					t.Errorf("%s with %q from %q: 403 %s, want no Origin check", method, cookie, origin, resp.Body.String())
				case slices.Contains(routed, method) && calls.Load() != before+1:
					t.Errorf("%s with %q from %q: status %d, the handler did not run", method, cookie, origin, resp.Code)
				}
			}
		}
	}
}

// Адреса — записи HTTP_CORS_ORIGINS без пробелов вокруг: «*» и «null» адресов не разрешают. Без адреса сервер без fx
// отвечает 403 каждому изменяющему запросу с cookie сессии: стартовой проверки у него нет.
func TestSessionCookies_ConfigOrigins(t *testing.T) {
	for name, tc := range map[string]struct {
		origins []string
		origin  string
		refused bool
	}{
		"any origin of CORS":     {[]string{"*", appOrigin}, "https://evil.example.com", true},
		"the app next to any":    {[]string{"*", appOrigin}, appOrigin, false},
		"null in the list":       {[]string{"null", appOrigin}, "null", true},
		"spaces around an entry": {[]string{" " + appOrigin + " "}, appOrigin, false},
		"second entry":           {[]string{adminOrigin, appOrigin}, appOrigin, false},
		"only any":               {[]string{"*"}, appOrigin, true},
		"no list":                {nil, appOrigin, true},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			api := cookieAPI(t, originConfig(t, tc.origins...), registerThings(&calls))
			resp := api.Post("/things", headers("sid="+sessionValue, tc.origin)...)
			if tc.refused {
				handlertest.AssertAPIError(t, resp, http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
				return
			}
			handlertest.AssertStatus(t, resp, http.StatusNoContent)
		})
	}
}

// Отказ — до тела, HumaAuth и extractor'а схемы: подделка с чужой страницы не доходит ни до проверки cookie, ни до
// лимита тела. Запрос без cookie сессии с Bearer и без Origin проходит; запрос с cookie и Bearer — с проверкой: cookie в
// нём есть.
func TestSessionCookies_BeforeAuth(t *testing.T) {
	var extracted, handled atomic.Int32
	extractor := func(present func(*http.Request) bool) httpkit.AuthExtractor {
		return func(r *http.Request) (context.Context, error) {
			extracted.Add(1)
			if !present(r) {
				return nil, errors.New("no credentials")
			}
			return httpkit.ContextWithSubject(r.Context(), "u1"), nil
		}
	}
	schemes := httpkit.WithSecuritySchemes(
		httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: bearerScheme, Extract: extractor(func(r *http.Request) bool {
			return r.Header.Get("Authorization") == "Bearer ok"
		})},
		httpkit.SecuritySchemeEntry{
			Name: "Cookie", Scheme: &huma.SecurityScheme{Type: "apiKey", In: "cookie", Name: "sid"},
			Extract: extractor(func(r *http.Request) bool {
				_, err := r.Cookie("sid")
				return err == nil
			}),
		},
	)
	cfg := originConfig(t, appOrigin)
	cfg.MaxBody.MaxBytes = 64
	type order struct {
		Body struct {
			Note string `json:"note"`
		}
	}
	api := cookieAPI(t, cfg, func(api huma.API) {
		huma.Register(api, huma.Operation{
			OperationID: "create-order", Method: http.MethodPost, Path: "/orders",
			Security: []map[string][]string{{"Cookie": {}}, {"Bearer": {}}},
		}, func(context.Context, *order) (*struct{}, error) {
			handled.Add(1)
			return nil, nil // тело ответа не нужно
		})
	}, schemes)
	body := `{"note":"` + strings.Repeat("x", 200) + `"}`
	small := `{"note":"x"}`
	cookie, bearer := "sid="+sessionValue, "Authorization: Bearer ok"

	for name, tc := range map[string]struct {
		headers []any
		body    string
		status  int
	}{
		"cookie without origin, over the body limit": {headers(cookie, ""), body, http.StatusForbidden},
		"cookie from a foreign page":                 {headers(cookie, "https://evil.example.com"), small, http.StatusForbidden},
		"cookie and bearer without origin":           {headers(cookie, "", bearer), small, http.StatusForbidden},
		"cookie from the app":                        {headers(cookie, appOrigin), small, http.StatusNoContent},
		"bearer without origin":                      {headers("", "", bearer), small, http.StatusNoContent},
		"bearer from a foreign page":                 {headers("", "https://evil.example.com", bearer), small, http.StatusNoContent},
		"cookie and bearer from the app":             {headers(cookie, appOrigin, bearer), small, http.StatusNoContent},
	} {
		t.Run(name, func(t *testing.T) {
			extractedBefore, handledBefore := extracted.Load(), handled.Load()
			resp := api.Post("/orders", append(slices.Clone(tc.headers), "Content-Type: application/json", strings.NewReader(tc.body))...)
			if tc.status != http.StatusForbidden {
				handlertest.AssertStatus(t, resp, tc.status)
				return
			}
			handlertest.AssertAPIError(t, resp, http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
			if extracted.Load() != extractedBefore || handled.Load() != handledBefore {
				t.Fatalf("the extractor ran %d times, the handler %d after the refusal",
					extracted.Load()-extractedBefore, handled.Load()-handledBefore)
			}
		})
	}
}

// Проверка — в цепочке кита, а не у операции huma: маршрут мимо huma и путь без маршрута тоже под ней.
func TestSessionCookies_RawRoute(t *testing.T) {
	s := mustServer(t, originConfig(t, appOrigin), httpkit.WithSessionCookies("sid"))
	var hooked atomic.Int32
	s.Router().Post("/hook", func(w http.ResponseWriter, _ *http.Request) {
		hooked.Add(1)
		w.WriteHeader(http.StatusAccepted)
	})
	api := humatest.Wrap(t, s.BindAPI())

	handlertest.AssertAPIError(t, api.Post("/hook", headers("sid="+sessionValue, "")...), http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
	handlertest.AssertAPIError(t, api.Post("/nowhere", headers("sid="+sessionValue, "")...), http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
	if hooked.Load() != 0 {
		t.Fatalf("the raw route ran %d times after the refusal", hooked.Load())
	}
	handlertest.AssertStatus(t, api.Post("/hook", headers("sid="+sessionValue, appOrigin)...), http.StatusAccepted)
	handlertest.AssertStatus(t, api.Post("/hook"), http.StatusAccepted)
	handlertest.AssertAPIError(t, api.Post("/nowhere", headers("sid="+sessionValue, appOrigin)...), http.StatusNotFound, "NOT_FOUND")
}

// Cookie сессии среди частей сверх предела net/http (GODEBUG httpcookiemaxnum, по умолчанию 3000) r.Cookie не видит, а
// вход huma `cookie:"…"` читает: проверка находит её и там — и во второй строке Cookie.
func TestSessionCookies_ManyCookieParts(t *testing.T) {
	s := mustServer(t, originConfig(t, appOrigin), httpkit.WithSessionCookies("sid"))
	var calls atomic.Int32
	registerThings(&calls)(s.BindAPI())
	api := humatest.Wrap(t, s.BindAPI())

	many := strings.Repeat("a=b; ", 3000) + "sid=" + sessionValue
	handlertest.AssertAPIError(t, api.Post("/things", "Cookie: "+many), http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
	handlertest.AssertAPIError(t, api.Post("/things", "Cookie: a=b", "Cookie: sid="+sessionValue), http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
	if calls.Load() != 0 {
		t.Fatalf("the handler ran %d times after the refusal", calls.Load())
	}
	handlertest.AssertStatus(t, api.Post("/things", "Cookie: "+many, "Origin: "+appOrigin), http.StatusNoContent)
}

// Имя, которого не бывает у cookie, запрос не принёс бы, и проверка молча не действовала бы: NewServer — ErrConfig с
// этим именем. Опция без имён ничего не включает.
func TestSessionCookies_Names(t *testing.T) {
	cfg := originConfig(t, appOrigin)
	for _, name := range []string{"", " sid", "sid ", "a=b", "a;b", "a,b", "a b", "сессия", "sid\t"} {
		_, err := httpkit.NewServer(cfg, logkit.Nop(), httpkit.WithSessionCookies("access_token", name))
		if !errors.Is(err, httpkit.ErrConfig) || !strings.Contains(err.Error(), "session cookie") {
			t.Errorf("session cookie %q: %v, want ErrConfig", name, err)
		}
	}
	for _, name := range []string{"sid", "access_token", "__Host-access_token", "stit_session"} {
		if _, err := httpkit.NewServer(cfg, logkit.Nop(), httpkit.WithSessionCookies(name)); err != nil {
			t.Errorf("session cookie %q: %v", name, err)
		}
	}
	var calls atomic.Int32
	off := mustServer(t, cfg, httpkit.WithSessionCookies())
	registerThings(&calls)(off.BindAPI())
	api := humatest.Wrap(t, off.BindAPI())
	handlertest.AssertStatus(t, api.Post("/things", headers("sid="+sessionValue, "")...), http.StatusNoContent)
}

// Без адреса в HTTP_CORS_ORIGINS Module не стартует — ErrConfig с именем переменной и cookie: каждый изменяющий запрос с
// ними получал бы 403. Граф без старта (сборка спецификации, проверка маршрутов) собирается.
func TestSessionCookies_NeedOrigin(t *testing.T) {
	for name, origins := range map[string][]string{
		"no list":      nil,
		"only any":     {"*"},
		"any and null": {"*", "null"},
		"blank entry":  {" "},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := originConfig(t, origins...)
			app := fx.New(fx.NopLogger, fx.Supply(cfg), fx.Provide(logkit.Nop),
				fx.Supply(httpkit.SessionCookies{"sid", "refresh"}), register(httpkit.Public(operation("create", http.MethodPost, "/things"))),
				httpkit.Module)
			if err := app.Err(); err != nil {
				t.Fatalf("the graph without a start: %v", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := app.Start(ctx)
			if err == nil {
				_ = app.Stop(ctx)
			}
			if !errors.Is(err, httpkit.ErrConfig) || !strings.Contains(err.Error(), "HTTP_CORS_ORIGINS") || !strings.Contains(err.Error(), "sid, refresh") {
				t.Fatalf("start: %v, want ErrConfig naming HTTP_CORS_ORIGINS and the cookies", err)
			}
		})
	}
}

// Module: значение графа SessionCookies включает проверку на живом сервере; без него запрос с cookie и без Origin
// проходит, как прежде.
func TestSessionCookies_Module(t *testing.T) {
	cfg := originConfig(t, appOrigin)
	if err := fx.ValidateApp(fx.NopLogger, fx.Supply(cfg), fx.Provide(logkit.Nop), fx.Supply(httpkit.SessionCookies{"sid"}), httpkit.Module); err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
	t.Setenv("HTTP_CORS_ORIGINS", appOrigin)
	routes := register(httpkit.Public(operation("create", http.MethodPost, "/things")), secured(operation("update", http.MethodPut, "/things")))
	srv := startApp(t, fx.Supply(httpkit.SessionCookies{"sid"}), withBearer, routes)
	cookie := "Cookie: sid=" + sessionValue

	resp := do(t, srv, http.MethodPost, "/things", nil, cookie)
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusForbidden || ct != httpkit.ProblemContentType ||
		!strings.Contains(bodyString(t, resp), `"code":"ORIGIN_NOT_ALLOWED"`) {
		t.Fatalf("POST with a session cookie without Origin: %d %s", resp.StatusCode, ct)
	}
	for _, c := range []struct {
		method  string
		headers []string
		status  int
	}{
		{http.MethodPost, []string{cookie, "Origin: " + appOrigin}, http.StatusNoContent},
		{http.MethodPut, []string{"Authorization: Bearer ok"}, http.StatusNoContent},
		{http.MethodPut, []string{cookie, "Authorization: Bearer ok", "Origin: https://evil.example.com"}, http.StatusForbidden},
	} {
		if got := do(t, srv, c.method, "/things", nil, c.headers...).StatusCode; got != c.status {
			t.Errorf("%s %v: %d, want %d", c.method, c.headers, got, c.status)
		}
	}
	if got := do(t, srv, http.MethodGet, "/health", nil, cookie).StatusCode; got != http.StatusOK {
		t.Errorf("GET /health with a session cookie without Origin: %d, want 200", got)
	}

	plain := startApp(t, withBearer, routes)
	if got := do(t, plain, http.MethodPost, "/things", nil, cookie).StatusCode; got != http.StatusNoContent {
		t.Errorf("without SessionCookies: POST with a cookie without Origin = %d, want 204", got)
	}
}

// Отказ виден: http_origin_rejections_total{path, reason} — missing без Origin, not_allowed с чужим (и null), путь без
// маршрута — unmatched; WARN с методом и Origin (длинный — обрезан), без значения cookie. Принятый запрос сигналов
// отказа не даёт.
func TestSessionCookies_Signals(t *testing.T) {
	before := metrictest.Collect(t)
	log := logtest.CaptureAt(slog.LevelWarn)
	s, err := httpkit.NewServer(originConfig(t, appOrigin), log, httpkit.WithSessionCookies("sid"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	registerThings(&calls)(s.BindAPI())
	api := humatest.Wrap(t, s.BindAPI())
	cookie := "sid=" + sessionValue
	long := "https://" + strings.Repeat("a.", 150) + "example"

	api.Post("/things", headers(cookie, "")...)
	api.Post("/things", headers(cookie, "https://evil.example.com")...)
	api.Delete("/things", headers(cookie, long)...)
	api.Post("/nowhere", headers(cookie, "null")...)
	api.Post("/things", headers(cookie, appOrigin)...)
	api.Post("/things", headers("", "https://evil.example.com")...)

	d := metrictest.Collect(t).Sub(before)
	for _, c := range []struct {
		path, reason string
		n            float64
	}{{"/things", "missing", 1}, {"/things", "not_allowed", 2}, {"unmatched", "not_allowed", 1}} {
		if got := d.Get(t, "http_origin_rejections_total", "path", c.path, "reason", c.reason); got.Kind != metrictest.Counter || got.Value != c.n {
			t.Errorf("http_origin_rejections_total{path=%s, reason=%s} = %v (%s), want %v", c.path, c.reason, got.Value, got.Kind, c.n)
		}
	}
	if got := d.Find("http_origin_rejections_total"); len(got) != 3 {
		t.Errorf("http_origin_rejections_total series: %v, want three", got)
	}

	var refusals []logtest.Entry
	for _, e := range log.Entries() {
		for k, v := range e.KV {
			if s, ok := v.(string); ok && strings.Contains(s, sessionValue) {
				t.Errorf("log line %q names the session cookie in %s", e.Msg, k)
			}
		}
		if e.Msg == "session cookie refused: origin is not allowed" {
			refusals = append(refusals, e)
		}
	}
	if len(refusals) != 4 {
		t.Fatalf("refusal lines: %+v, want four", refusals)
	}
	for i, want := range []struct{ method, origin string }{
		{http.MethodPost, ""}, {http.MethodPost, "https://evil.example.com"}, {http.MethodDelete, long[:256] + "…"}, {http.MethodPost, "null"},
	} {
		if e := refusals[i]; e.Level != slog.LevelWarn || e.Str("method") != want.method || e.Str("origin") != want.origin {
			t.Errorf("refusal %d: %v %+v, want Warn with method %s and origin %q", i, e.Level, e.KV, want.method, want.origin)
		}
	}
}

// 403 ORIGIN_NOT_ALLOWED — в OpenAPI каждой операции изменяющего метода, которой его отдаёт проверка: операции huma,
// группы huma и мимо huma с описанием руками, — после кодов 403 из Declare; у безопасного метода и у сервера без cookie
// сессии его нет. Описание — то, что кит отвечает.
func TestSessionCookies_OpenAPI(t *testing.T) {
	type named struct {
		Body struct {
			Name string `json:"name"`
		}
	}
	register := func(api huma.API) {
		huma.Register(api, httpkit.Public(operation("create", http.MethodPost, "/orders")),
			func(context.Context, *named) (*struct{}, error) { return nil, nil })
		huma.Register(api, userErrors.Declare(secured(operation("replace", http.MethodPut, "/orders"))), noBody)
		huma.Register(api, secured(operation("amend", http.MethodPatch, "/orders")), noBody)
		huma.Register(api, secured(operation("drop", http.MethodDelete, "/orders")), noBody)
		huma.Register(api, secured(operation("list", http.MethodGet, "/orders")), noBody)
		huma.Register(api, httpkit.Public(operation("peek", http.MethodHead, "/orders")), noBody)
		sse.Register(api, httpkit.Public(operation("events", http.MethodGet, "/events")),
			map[string]any{"tick": struct{}{}}, func(context.Context, *struct{}, sse.Sender) {})
		huma.Register(huma.NewGroup(api, "/v1"), httpkit.Public(operation("gcreate", http.MethodPost, "/orders")), noBody)
		api.OpenAPI().AddOperation(&huma.Operation{OperationID: "rawRoute", Method: http.MethodPost, Path: "/raw-route", Security: bearerOnly})
	}
	cfg := originConfig(t, appOrigin)
	schemes := handlertest.WithSecuritySchemes(httpkit.SecuritySchemeEntry{
		Name: "Bearer", Scheme: bearerScheme,
		Extract: func(r *http.Request) (context.Context, error) {
			return httpkit.ContextWithSubject(r.Context(), "u1"), nil
		},
	})
	const refused = "Forbidden: ORIGIN_NOT_ALLOWED"

	api := handlertest.NewAPI(t, register, handlertest.WithConfig(cfg), schemes, handlertest.WithSessionCookies("sid"))
	ops := operations(api.OpenAPI())
	for id, want := range map[string]string{
		"create": refused, "replace": "Forbidden: FORBIDDEN, ORIGIN_NOT_ALLOWED", "amend": refused, "drop": refused,
		"gcreate": refused, "rawRoute": refused, "list": "", "events": "",
	} {
		resp, got := ops[id].Responses["403"], ""
		if resp != nil {
			got = resp.Description
		}
		if got != want {
			t.Errorf("%s: 403 = %q, want %q", id, got, want)
		}
		if resp != nil && !strings.HasSuffix(resp.Content[httpkit.ProblemContentType].Schema.Ref, "/APIError") {
			t.Errorf("%s: 403 is not an APIError", id)
		}
	}
	if head := api.OpenAPI().Paths["/orders"].Head; head == nil || head.Responses["403"] != nil {
		t.Errorf("HEAD /orders declares 403: %+v", head)
	}
	resp := api.Post("/orders", "Cookie: sid="+sessionValue, map[string]any{"name": "x"})
	handlertest.AssertAPIError(t, resp, http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
	if codes := codesOf(ops["create"], http.StatusForbidden); !slices.Contains(codes, "ORIGIN_NOT_ALLOWED") {
		t.Errorf("the kit answers 403 ORIGIN_NOT_ALLOWED, the contract of create lists %v", codes)
	}

	plain := handlertest.NewAPI(t, register, handlertest.WithConfig(cfg), schemes)
	for id, op := range operations(plain.OpenAPI()) {
		got, want := "", ""
		if id == "replace" {
			want = "Forbidden: FORBIDDEN"
		}
		if resp := op.Responses["403"]; resp != nil {
			got = resp.Description
		}
		if got != want {
			t.Errorf("without session cookies: %s 403 = %q, want %q", id, got, want)
		}
	}
}
