package oidckit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/internal/jwks"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/oidctest"
)

const sessionSecret = "session-secret-of-at-least-32-bytes!!"

func memStore() StateStore { return NewCacheStateStore(cachekit.NewMemoryCache()) }

func sessionSigner(t *testing.T) *authnkit.HS256JWT {
	t.Helper()
	s, err := authnkit.NewHS256JWT(authnkit.JWTConfig{Secret: sessionSecret})
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	return s
}

func testConfig(idp *oidctest.IDP) Config {
	return Config{
		Issuer:              idp.URL(),
		ClientID:            idp.ClientID(),
		ClientSecret:        "cs",
		RedirectURL:         "http://app.test/auth/callback",
		PostLoginURL:        "/app",
		SessionTTL:          12 * time.Hour,
		Leeway:              time.Minute,
		AllowInsecureIssuer: true,
	}
}

// devCookies — политика без Secure: тестам удобнее cookie без префикса __Host-.
var devCookies = CookieConfig{Insecure: true, SameSite: SameSiteLax}

func newTestBFF(t *testing.T, idp *oidctest.IDP, store StateStore, hooks Hooks) *BFF {
	t.Helper()
	b, err := NewBFF(testConfig(idp), idp.Client(), sessionSigner(t), store, devCookies, hooks, logkit.Nop())
	if err != nil {
		t.Fatalf("NewBFF: %v", err)
	}
	return b
}

func parseSession(t *testing.T, token string) map[string]any {
	t.Helper()
	claims, err := sessionSigner(t).Parse(t.Context(), token)
	if err != nil {
		t.Fatalf("parse session: %v", err)
	}
	return claims
}

// loginFlow проводит login и /authorize stub-провайдера: возвращает code и
// state ровно так, как их принесёт браузер, плюс выданную flow-cookie.
func loginFlow(t *testing.T, b *BFF, idp *oidctest.IDP, returnTo string) (code, state, flowCookie string) {
	t.Helper()
	out, err := b.login(t.Context(), &loginInput{ReturnTo: returnTo})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if len(out.SetCookie) != 1 || out.SetCookie[0].Name != devCookies.StateCookieName() {
		t.Fatalf("login не выставил flow-cookie: %+v", out.SetCookie)
	}
	flowCookie = out.SetCookie[0].Value

	client := idp.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(out.Location)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("callback location: %v", err)
	}
	code, state = loc.Query().Get("code"), loc.Query().Get("state")
	if code == "" || state == "" {
		t.Fatalf("stub не вернул code/state: %s", resp.Header.Get("Location"))
	}
	return code, state, flowCookie
}

func callbackWith(code, state, cookie string) *callbackInput {
	return &callbackInput{Code: code, State: state, StateCookiePlain: cookie}
}

func TestBFF_LoginCallback(t *testing.T) {
	idp := oidctest.New(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})

	code, state, flow := loginFlow(t, b, idp, "")
	if flow != state {
		t.Fatalf("flow-cookie=%q, state=%q — не связаны", flow, state)
	}

	out, err := b.callback(t.Context(), callbackWith(code, state, flow))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if out.Location != "/app" || out.CacheControl != noStore {
		t.Fatalf("out=%+v", out)
	}
	if len(out.SetCookie) != 2 {
		t.Fatalf("cookies=%+v", out.SetCookie)
	}
	session := out.SetCookie[0]
	if session.Name != CookieAccess || !session.HttpOnly || session.MaxAge != int((12*time.Hour).Seconds()) {
		t.Fatalf("session cookie: %+v", session)
	}
	if flowReset := out.SetCookie[1]; flowReset.Name != CookieState || flowReset.MaxAge != -1 {
		t.Fatalf("flow-cookie не сброшена: %+v", flowReset)
	}

	claims := parseSession(t, session.Value)
	for k, want := range map[string]string{
		"sub":                   oidctest.DefaultSubject,
		"preferred_username":    oidctest.DefaultUsername,
		"name":                  oidctest.DefaultName,
		"email":                 oidctest.DefaultEmail,
		authnkit.ClaimTokenType: SessionTokenType,
	} {
		if claims[k] != want {
			t.Fatalf("claim %s=%v, ожидали %q", k, claims[k], want)
		}
	}

	// state одноразовый — второй callback отвергается.
	if _, err := b.callback(t.Context(), callbackWith(code, state, flow)); err == nil {
		t.Fatal("повторный callback с тем же state прошёл")
	}
}

// Login-CSRF: callback без flow-cookie или с чужой — отказ до похода в store.
func TestBFF_Callback_FlowCookie(t *testing.T) {
	cases := map[string]func(state, flow string) *callbackInput{
		"без flow-cookie":   func(state, _ string) *callbackInput { return callbackWith("code", state, "") },
		"чужая flow-cookie": func(state, _ string) *callbackInput { return callbackWith("code", state, "attacker-state") },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			idp := oidctest.New(t)
			b := newTestBFF(t, idp, memStore(), Hooks{})
			code, state, flow := loginFlow(t, b, idp, "")
			_ = code

			if _, err := b.callback(t.Context(), build(state, flow)); err == nil {
				t.Fatal("callback без привязки к браузеру прошёл")
			}
			// state остался в store: отказ произошёл до GetDel.
			if _, err := b.callback(t.Context(), callbackWith(code, state, flow)); err != nil {
				t.Fatalf("легитимный callback после отказа: %v", err)
			}
		})
	}
}

func TestSanitizeReturnTo(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"внутренний путь", "/join/abc", "/join/abc"},
		{"путь с query и fragment", "/a?b=c#d", "/a?b=c#d"},
		{"пусто", "", ""},
		{"внешний URL", "https://evil.example/x", ""},
		{"javascript-схема", "javascript:alert(1)", ""},
		{"protocol-relative", "//evil.example/x", ""},
		{"backslash-relative", "/\\evil.example/x", ""},
		{"TAB перед protocol-relative", "/\t//evil.example/x", ""},
		{"TAB перед хостом", "/\t/evil.example/x", ""},
		{"percent-encoded TAB", "/%09//evil.example/x", ""},
		{"percent-encoded слеши", "/%2F%2Fevil.example/x", ""},
		{"CRLF-инъекция", "/x\r\nX-Injected: 1", ""},
		{"NUL", "/x\x00y", ""},
		{"слишком длинный", "/" + string(make([]byte, MaxReturnToLen)), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeReturnTo(tc.in); got != tc.want {
				t.Fatalf("sanitizeReturnTo(%q)=%q, ожидали %q", tc.in, got, tc.want)
			}
		})
	}
}

// return_to доезжает до redirect'а callback'а; небезопасные значения заменяются
// на OIDC_POST_LOGIN_URL.
func TestBFF_ReturnTo(t *testing.T) {
	cases := []struct {
		name, returnTo, want string
	}{
		{"внутренний путь", "/join/abc", "/join/abc"},
		{"внешний URL отброшен", "https://evil.example/x", "/app"},
		{"protocol-relative отброшен", "//evil.example/x", "/app"},
		{"TAB-обход отброшен", "/\t//evil.example/x", "/app"},
		{"backslash отброшен", "/\\evil.example/x", "/app"},
		{"пусто — дефолт", "", "/app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := oidctest.New(t)
			b := newTestBFF(t, idp, memStore(), Hooks{})

			code, state, flow := loginFlow(t, b, idp, tc.returnTo)
			out, err := b.callback(t.Context(), callbackWith(code, state, flow))
			if err != nil {
				t.Fatalf("callback: %v", err)
			}
			if out.Location != tc.want {
				t.Fatalf("location=%q, ожидали %q", out.Location, tc.want)
			}
		})
	}
}

// Hooks.SessionClaims дополняет session-JWT продуктовыми claims, но не может
// подменить служебный typ.
func TestBFF_SessionClaimsHook(t *testing.T) {
	idp := oidctest.New(t)
	idp.SetClaims(map[string]any{"groups": []any{"admins"}})
	hooks := Hooks{SessionClaims: func(c Claims) map[string]any {
		role := "client"
		for _, g := range c.Groups {
			if g == "admins" {
				role = "admin"
			}
		}
		return map[string]any{"role": role, authnkit.ClaimTokenType: "forged"}
	}}
	b := newTestBFF(t, idp, memStore(), hooks)

	code, state, flow := loginFlow(t, b, idp, "")
	out, err := b.callback(t.Context(), callbackWith(code, state, flow))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	claims := parseSession(t, out.SetCookie[0].Value)
	if claims["role"] != "admin" {
		t.Fatalf("role=%v, ожидали admin", claims["role"])
	}
	if claims[authnkit.ClaimTokenType] != SessionTokenType {
		t.Fatalf("hook подменил typ: %v", claims[authnkit.ClaimTokenType])
	}
}

// Hooks.OnLogin выполняется до выдачи cookie; ошибка отменяет вход.
func TestBFF_OnLoginHook(t *testing.T) {
	run := func(t *testing.T, hookErr error) (bool, error) {
		t.Helper()
		called := false
		idp := oidctest.New(t)
		hooks := Hooks{OnLogin: func(_ context.Context, c Claims) error {
			called = true
			if c.Subject != oidctest.DefaultSubject {
				t.Errorf("hook claims: %+v", c)
			}
			return hookErr
		}}
		b := newTestBFF(t, idp, memStore(), hooks)
		code, state, flow := loginFlow(t, b, idp, "")
		_, err := b.callback(t.Context(), callbackWith(code, state, flow))
		return called, err
	}

	t.Run("успех — hook вызван, вход прошёл", func(t *testing.T) {
		called, err := run(t, nil)
		if !called || err != nil {
			t.Fatalf("called=%v err=%v", called, err)
		}
	})
	t.Run("ошибка hook'а отменяет вход", func(t *testing.T) {
		called, err := run(t, context.DeadlineExceeded)
		if !called || err == nil {
			t.Fatalf("called=%v err=%v", called, err)
		}
	})
}

type brokenStore struct{ err error }

func (s brokenStore) Set(context.Context, string, []byte, time.Duration) error { return s.err }
func (s brokenStore) GetDel(context.Context, string) ([]byte, bool, error)     { return nil, false, s.err }

type brokenSigner struct{ authnkit.JWTSigner }

func (brokenSigner) Sign(context.Context, map[string]any, time.Duration) (string, error) {
	return "", errors.New("sign failed")
}

func TestBFF_Callback_Errors(t *testing.T) {
	t.Run("неизвестный state", func(t *testing.T) {
		idp := oidctest.New(t)
		b := newTestBFF(t, idp, memStore(), Hooks{})
		if _, err := b.callback(t.Context(), callbackWith("code", "nope", "nope")); err == nil {
			t.Fatal("callback с неизвестным state прошёл")
		}
	})

	t.Run("пустые code/state", func(t *testing.T) {
		idp := oidctest.New(t)
		b := newTestBFF(t, idp, memStore(), Hooks{})
		if _, err := b.callback(t.Context(), callbackWith("", "", "")); err == nil {
			t.Fatal("callback без параметров прошёл")
		}
	})

	t.Run("испорченный state в store", func(t *testing.T) {
		idp := oidctest.New(t)
		store := memStore()
		b := newTestBFF(t, idp, store, Hooks{})
		if err := store.Set(t.Context(), stateKeyPrefix+"corrupt", []byte("{"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if _, err := b.callback(t.Context(), callbackWith("code", "corrupt", "corrupt")); err == nil {
			t.Fatal("испорченный state прошёл")
		}
	})

	t.Run("store недоступен", func(t *testing.T) {
		idp := oidctest.New(t)
		b := newTestBFF(t, idp, brokenStore{err: errors.New("redis down")}, Hooks{})
		if _, err := b.login(t.Context(), &loginInput{}); err == nil {
			t.Fatal("login при недоступном store прошёл")
		}
		if _, err := b.callback(t.Context(), callbackWith("code", "state", "state")); err == nil {
			t.Fatal("callback при недоступном store прошёл")
		}
	})

	t.Run("token endpoint недоступен", func(t *testing.T) {
		idp := oidctest.New(t)
		b := newTestBFF(t, idp, memStore(), Hooks{})
		code, state, flow := loginFlow(t, b, idp, "")
		idp.SetStatus(oidctest.EndpointToken, http.StatusInternalServerError)

		if _, err := b.callback(t.Context(), callbackWith(code, state, flow)); err == nil {
			t.Fatal("callback при сломанном exchange прошёл")
		}
	})

	t.Run("discovery сломалось между login и callback", func(t *testing.T) {
		idp := oidctest.New(t)
		b := newTestBFF(t, idp, memStore(), Hooks{})
		code, state, flow := loginFlow(t, b, idp, "")

		b.v.Store(nil)
		idp.SetStatus(oidctest.EndpointDiscovery, http.StatusInternalServerError)
		if _, err := b.callback(t.Context(), callbackWith(code, state, flow)); err == nil {
			t.Fatal("callback при сломанном discovery прошёл")
		}
	})

	t.Run("подпись сессии не удалась", func(t *testing.T) {
		idp := oidctest.New(t)
		b, err := NewBFF(testConfig(idp), idp.Client(), brokenSigner{}, memStore(), devCookies, Hooks{}, logkit.Nop())
		if err != nil {
			t.Fatalf("NewBFF: %v", err)
		}
		code, state, flow := loginFlow(t, b, idp, "")
		if _, err := b.callback(t.Context(), callbackWith(code, state, flow)); err == nil {
			t.Fatal("callback без подписи сессии прошёл")
		}
	})
}

// Пока discovery не удаётся, параллельные login'ы не устраивают шторм запросов
// к недоступному idp: одна попытка на окно отрицательного кэша.
func TestBFF_DiscoveryBackoff(t *testing.T) {
	idp := oidctest.New(t)
	idp.SetStatus(oidctest.EndpointDiscovery, http.StatusInternalServerError)
	b := newTestBFF(t, idp, memStore(), Hooks{})

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := b.login(t.Context(), &loginInput{}); err == nil {
				t.Error("login при недоступном discovery прошёл")
			}
		})
	}
	wg.Wait()
	if got := idp.Hits(oidctest.EndpointDiscovery); got != 1 {
		t.Fatalf("20 параллельных login → %d запросов discovery, ожидали 1", got)
	}
}

func TestBFF_Probe(t *testing.T) {
	idp := oidctest.New(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})

	probe := b.Probe()
	if probe.Name != "oidckit" {
		t.Fatalf("probe name=%q", probe.Name)
	}
	if err := probe.Run(t.Context()); err != nil {
		t.Fatalf("проба на живом idp: %v", err)
	}

	broken := oidctest.New(t)
	broken.SetStatus(oidctest.EndpointJWKS, http.StatusServiceUnavailable)
	bb := newTestBFF(t, broken, memStore(), Hooks{})
	if err := bb.Probe().Run(t.Context()); err == nil {
		t.Fatal("проба при недоступном JWKS прошла")
	}

	// Discovery выполнен, а ключей нет — не готов.
	empty := newTestBFF(t, idp, memStore(), Hooks{})
	empty.v.Store(&Verifier{keys: jwks.New(idp.JWKSURL(), idp.Client())})
	if err := empty.Probe().Run(t.Context()); !errors.Is(err, ErrNoJWKS) {
		t.Fatalf("проба без загруженных ключей: %v", err)
	}
}

// Warmup укладывается в дедлайн вызывающего: fx-хук старта не должен висеть
// дольше своего таймаута, пока idp молчит.
func TestBFF_WarmupRespectsDeadline(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(hang) })

	b, err := NewBFF(Config{
		Issuer:              srv.URL,
		ClientID:            "cid",
		RedirectURL:         "http://app.test/auth/callback",
		SessionTTL:          time.Hour,
		AllowInsecureIssuer: true,
	}, srv.Client(), sessionSigner(t), memStore(), devCookies, Hooks{}, logkit.Nop())
	if err != nil {
		t.Fatalf("NewBFF: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := b.Warmup(ctx); err == nil {
		t.Fatal("Warmup на молчащем idp вернул nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Warmup вернулся через %s: дедлайн ctx не учтён", elapsed)
	}
}

// Дождавшийся чужой попытки discovery уходит по своему дедлайну, а сама попытка
// доводится до конца: один короткий запрос не отменяет её для остальных.
func TestBFF_VerifierRespectsJoinerDeadline(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			w.WriteHeader(http.StatusInternalServerError)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	b, err := NewBFF(Config{
		Issuer:              srv.URL,
		ClientID:            "cid",
		RedirectURL:         "http://app.test/auth/callback",
		SessionTTL:          time.Hour,
		AllowInsecureIssuer: true,
	}, srv.Client(), sessionSigner(t), memStore(), devCookies, Hooks{}, logkit.Nop())
	if err != nil {
		t.Fatalf("NewBFF: %v", err)
	}

	leader := make(chan error, 1)
	go func() { leader <- b.Warmup(context.WithoutCancel(t.Context())) }()
	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := b.Warmup(ctx); err == nil {
		t.Fatal("присоединившийся Warmup вернул nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("присоединившийся ждал %s: дедлайн ctx не учтён", elapsed)
	}

	close(release)
	if err := <-leader; err == nil {
		t.Fatal("ведущий Warmup вернул nil на сломанном discovery")
	}
}

// Пока окно отрицательного кэша не истекло, повторная попытка отвечает прошлой
// ошибкой и в сеть не идёт.
func TestBFF_DiscoveryNegativeCache(t *testing.T) {
	idp := oidctest.New(t)
	idp.SetStatus(oidctest.EndpointDiscovery, http.StatusInternalServerError)
	b := newTestBFF(t, idp, memStore(), Hooks{})

	if _, err := b.login(t.Context(), &loginInput{}); err == nil {
		t.Fatal("login при недоступном discovery прошёл")
	}
	if got := idp.Hits(oidctest.EndpointDiscovery); got != 1 {
		t.Fatalf("первая попытка: %d запросов discovery", got)
	}

	// idp уже починился, но окно ещё держит прошлую ошибку.
	idp.SetStatus(oidctest.EndpointDiscovery, 0)
	if _, err := b.login(t.Context(), &loginInput{}); err == nil {
		t.Fatal("login внутри окна отрицательного кэша прошёл")
	}
	if got := idp.Hits(oidctest.EndpointDiscovery); got != 1 {
		t.Fatalf("второй login сходил в сеть: %d запросов discovery", got)
	}
}

// user_id из сессии попадает в каждый log-record; без сессии поля нет.
func TestRegisterIdentityLogAttr(t *testing.T) {
	var buf strings.Builder
	log, err := logkit.New(logkit.Config{Level: "info", Format: "json"},
		logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatalf("logkit.New: %v", err)
	}
	RegisterIdentityLogAttr()

	log.Info(WithIdentity(t.Context(), Identity{Subject: "u-1"}), "with session")
	log.Info(t.Context(), "without session")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("записей: %d (%s)", len(lines), buf.String())
	}
	first := map[string]any{}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode: %v (%s)", err, lines[0])
	}
	if first["user_id"] != "u-1" {
		t.Fatalf("user_id=%v в записи %s", first["user_id"], lines[0])
	}
	if strings.Contains(lines[1], "user_id") {
		t.Fatalf("user_id без сессии: %s", lines[1])
	}
}

func TestBFF_Warmup(t *testing.T) {
	idp := oidctest.New(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})
	if err := b.Warmup(t.Context()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	if got := idp.Hits(oidctest.EndpointDiscovery); got != 1 {
		t.Fatalf("discovery-запросов: %d", got)
	}
	// Повторный login не ходит в discovery заново.
	if _, err := b.login(t.Context(), &loginInput{}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := idp.Hits(oidctest.EndpointDiscovery); got != 1 {
		t.Fatalf("discovery повторился: %d", got)
	}
}

func TestBFF_Me(t *testing.T) {
	idp := oidctest.New(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})
	ctx := WithIdentity(t.Context(), Identity{Subject: "u-1", Username: "j.doe", Name: "Jane Doe", Role: "admin"})

	out, err := b.me(ctx, &struct{}{})
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	if out.CacheControl != noStore {
		t.Fatalf("me без no-store: %+v", out)
	}
	if d := out.Body.Data; d.Subject != "u-1" || d.Username != "j.doe" || d.Role != "admin" {
		t.Fatalf("me: %+v", d)
	}

	if _, err := b.me(t.Context(), &struct{}{}); err == nil {
		t.Fatal("me без identity прошёл")
	}
	if _, err := b.me(WithIdentity(t.Context(), Identity{}), &struct{}{}); err == nil {
		t.Fatal("me с пустым sub прошёл")
	}
}

// MustIdentityFrom — контракт защищённой операции: без Extractor'а это ошибка
// проводки, а не 401 в рантайме.
func TestMustIdentityFrom(t *testing.T) {
	ctx := WithIdentity(t.Context(), Identity{Subject: "u-1"})
	if id := MustIdentityFrom(ctx); id.Subject != "u-1" {
		t.Fatalf("identity: %+v", id)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("MustIdentityFrom без identity не паникует")
		}
	}()
	MustIdentityFrom(t.Context())
}

func TestBFF_Logout(t *testing.T) {
	idp := oidctest.New(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})

	cases := []struct {
		name  string
		in    logoutInput
		allow bool
	}{
		{"same-origin", logoutInput{SecFetchSite: "same-origin"}, true},
		{"переход по ссылке", logoutInput{SecFetchSite: "none"}, true},
		{"cross-site форма", logoutInput{SecFetchSite: "cross-site", Origin: "https://evil.example"}, false},
		{"чужой Origin без Sec-Fetch-Site", logoutInput{Origin: "https://evil.example"}, false},
		{"свой Origin без Sec-Fetch-Site", logoutInput{Origin: "http://app.test"}, true},
		{"не-браузерный клиент", logoutInput{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := b.logout(t.Context(), &tc.in)
			if !tc.allow {
				if err == nil {
					t.Fatal("cross-site logout прошёл")
				}
				return
			}
			if err != nil {
				t.Fatalf("logout: %v", err)
			}
			if out.CacheControl != noStore || len(out.SetCookie) == 0 {
				t.Fatalf("out=%+v", out)
			}
			for _, c := range out.SetCookie {
				if c.MaxAge != -1 {
					t.Fatalf("cookie %s: MaxAge=%d, ожидали -1", c.Name, c.MaxAge)
				}
			}
		})
	}
}

func TestNewBFF_InvalidConfig(t *testing.T) {
	idp := oidctest.New(t)
	cfg := testConfig(idp)
	cfg.ClientID = ""

	if _, err := NewBFF(cfg, idp.Client(), sessionSigner(t), memStore(), devCookies, Hooks{}, logkit.Nop()); !errors.Is(err, ErrConfig) {
		t.Fatalf("ожидали ErrConfig, получили %v", err)
	}
	if _, err := NewBFF(testConfig(idp), idp.Client(), nil, memStore(), devCookies, Hooks{}, logkit.Nop()); !errors.Is(err, ErrConfig) {
		t.Fatalf("BFF без signer'а: ожидали ErrConfig, получили %v", err)
	}
	badCookies := CookieConfig{SameSite: "sometimes"}
	if _, err := NewBFF(testConfig(idp), idp.Client(), sessionSigner(t), memStore(), badCookies, Hooks{}, logkit.Nop()); !errors.Is(err, ErrConfig) {
		t.Fatalf("невалидный CookieConfig: ожидали ErrConfig, получили %v", err)
	}

	// Без Doer'а кит заводит собственный клиент — с таймаутом, не DefaultClient.
	b, err := NewBFF(testConfig(idp), nil, sessionSigner(t), memStore(), devCookies, Hooks{}, logkit.Nop())
	if err != nil {
		t.Fatalf("NewBFF без doer'а: %v", err)
	}
	client, ok := b.doer.(*http.Client)
	if !ok || client.Timeout != defaultHTTPTimeout {
		t.Fatalf("fallback-клиент: %#v", b.doer)
	}
}
