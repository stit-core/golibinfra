package oidckit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
	"git.stit.tech/stit-core/golibinfra/testkit/oidctest"
)

// newAPI поднимает роуты кита за настоящим httpkit.HumaAuth со схемой Cookie: e2e проверяет и
// связку extractor'а с гейтом защищённых операций.
func newAPI(t *testing.T, b *BFF, e *Extractor) handlertest.API {
	t.Helper()
	scheme := NewSecurityScheme(b.cookie)
	scheme.Extract = e.Extract
	return handlertest.NewAPI(t, func(api huma.API) { RegisterRoutes(api, b) }, handlertest.WithSecuritySchemes(scheme))
}

func cookieHeader(t *testing.T, resp *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, c := range (&http.Response{Header: resp.Header()}).Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	t.Fatalf("в ответе нет cookie %s: %v", name, resp.Header().Values("Set-Cookie"))
	return ""
}

// При схеме Cookie в графе роуты BFF проходят проверку старта httpkit: login, callback и
// logout — httpkit.Public, me — под Security.
func TestRoutes_PassesServerRouteCheck(t *testing.T) {
	idp := oidctest.New(t)
	b, err := NewBFF(testConfig(idp), idp.Client(), sessionSigner(t), memStore(), devCookies, Hooks{}, logkit.Nop())
	if err != nil {
		t.Fatalf("NewBFF: %v", err)
	}
	cfg, err := httpkit.NewConfig()
	if err != nil {
		t.Fatalf("httpkit config: %v", err)
	}
	srv, err := httpkit.NewServer(cfg, logkit.Nop(), httpkit.WithSecuritySchemes(NewSecurityScheme(devCookies)))
	if err != nil {
		t.Fatalf("httpkit server: %v", err)
	}
	RegisterRoutes(srv.API(), b)
	if err := srv.CheckRoutes(); err != nil {
		t.Fatalf("BFF routes fail the httpkit start check: %v", err)
	}
}

// Полный проход BFF через huma: login → idp → callback → me → logout. Обе
// политики cookie: у прода имена с префиксом __Host-, и callback читает
// flow-cookie именно по ним.
func TestRoutes_EndToEnd(t *testing.T) {
	policies := map[string]CookieConfig{
		"dev по http":   devCookies,
		"прод (Secure)": {},
	}
	for name, cookies := range policies {
		t.Run(name, func(t *testing.T) {
			idp := oidctest.New(t)
			b, err := NewBFF(testConfig(idp), idp.Client(), sessionSigner(t), memStore(), cookies, Hooks{}, logkit.Nop())
			if err != nil {
				t.Fatalf("NewBFF: %v", err)
			}
			api := newAPI(t, b, NewExtractor(sessionSigner(t), cookies))
			stateName, sessionName := cookies.StateCookieName(), cookies.SessionCookieName()
			if hostPrefix := strings.HasPrefix(stateName, "__Host-"); hostPrefix != cookies.Secure() {
				t.Fatalf("имя flow-cookie %q не соответствует политике Secure=%v", stateName, cookies.Secure())
			}

			login := api.Get("/auth/login?return_to=/join/x")
			handlertest.AssertStatus(t, login, http.StatusFound)
			if login.Header().Get("Cache-Control") != noStore {
				t.Fatalf("login без no-store: %v", login.Header())
			}
			flow := cookieHeader(t, login, stateName)

			client := idp.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			authorized, err := client.Get(login.Header().Get("Location"))
			if err != nil {
				t.Fatalf("authorize: %v", err)
			}
			_ = authorized.Body.Close()
			loc, err := url.Parse(authorized.Header.Get("Location"))
			if err != nil {
				t.Fatalf("callback location: %v", err)
			}

			callback := api.Get("/auth/callback?"+loc.RawQuery, "Cookie: "+stateName+"="+flow)
			handlertest.AssertStatus(t, callback, http.StatusFound)
			if callback.Header().Get("Location") != "/join/x" || callback.Header().Get("Cache-Control") != noStore {
				t.Fatalf("callback headers: %v", callback.Header())
			}
			session := cookieHeader(t, callback, sessionName)
			if secure := strings.Contains(strings.Join(callback.Header().Values("Set-Cookie"), " "), "Secure"); secure != cookies.Secure() {
				t.Fatalf("Secure=%v при политике %v: %v", secure, cookies.Secure(), callback.Header().Values("Set-Cookie"))
			}

			me := api.Get("/auth/me", "Cookie: "+sessionName+"="+session)
			handlertest.AssertStatus(t, me, http.StatusOK)
			if me.Header().Get("Cache-Control") != noStore {
				t.Fatalf("me без no-store: %v", me.Header())
			}
			var body struct {
				Data MeView `json:"data"`
			}
			if err := json.Unmarshal(me.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode me: %v (%s)", err, me.Body)
			}
			if body.Data.Subject != oidctest.DefaultSubject {
				t.Fatalf("me: %+v", body.Data)
			}

			handlertest.AssertStatus(t, api.Get("/auth/me"), http.StatusUnauthorized)

			logout := api.Post("/auth/logout", "Sec-Fetch-Site: same-origin")
			handlertest.AssertStatus(t, logout, http.StatusNoContent)
			if !strings.Contains(strings.Join(logout.Header().Values("Set-Cookie"), " "), "Max-Age=0") {
				t.Fatalf("logout не сбросил cookie: %v", logout.Header().Values("Set-Cookie"))
			}
		})
	}
}

func TestRoutes_CallbackWithoutFlowCookie(t *testing.T) {
	idp := oidctest.New(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})
	api := newAPI(t, b, NewExtractor(sessionSigner(t), devCookies))

	code, state, _ := loginFlow(t, b, idp, "")
	resp := api.Get("/auth/callback?code=" + code + "&state=" + state)
	handlertest.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestRoutes_LogoutCrossSite(t *testing.T) {
	idp := oidctest.New(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})
	api := newAPI(t, b, NewExtractor(sessionSigner(t), devCookies))

	resp := api.Post("/auth/logout", "Sec-Fetch-Site: cross-site", "Origin: https://evil.example")
	handlertest.AssertStatus(t, resp, http.StatusForbidden)
	if len(resp.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("cross-site logout сбросил cookie: %v", resp.Header().Values("Set-Cookie"))
	}
}

// Лимиты длины query-параметров валидирует huma — до хендлера и до Redis.
func TestRoutes_InputLimits(t *testing.T) {
	idp := oidctest.New(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})
	api := newAPI(t, b, NewExtractor(sessionSigner(t), devCookies))

	long := strings.Repeat("a", MaxReturnToLen+1)
	handlertest.AssertStatus(t, api.Get("/auth/login?return_to=/"+long), http.StatusUnprocessableEntity)
	handlertest.AssertStatus(t, api.Get("/auth/callback?code=x&state="+strings.Repeat("s", MaxStateLen+1)), http.StatusUnprocessableEntity)
}

func TestRoutes_RegisteredOperations(t *testing.T) {
	idp := oidctest.New(t)
	cfg := testConfig(idp)
	cfg.RoutePrefix = "/sso"
	b, err := NewBFF(cfg, idp.Client(), sessionSigner(t), memStore(), devCookies, Hooks{}, logkit.Nop())
	if err != nil {
		t.Fatalf("NewBFF: %v", err)
	}
	api := newAPI(t, b, NewExtractor(sessionSigner(t), devCookies))

	ops := map[string]string{}
	for path, item := range api.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil {
				continue
			}
			if prev, dup := ops[op.OperationID]; dup {
				t.Fatalf("дублирующийся OperationID %s: %s и %s", op.OperationID, prev, path)
			}
			ops[op.OperationID] = path
		}
	}
	for id, want := range map[string]string{
		"oidcLogin":    "/sso/login",
		"oidcCallback": "/sso/callback",
		"oidcMe":       "/sso/me",
		"oidcLogout":   "/sso/logout",
	} {
		if ops[id] != want {
			t.Fatalf("операция %s зарегистрирована на %q, ожидали %q", id, ops[id], want)
		}
	}
}
