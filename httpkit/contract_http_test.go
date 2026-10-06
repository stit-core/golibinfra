package httpkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

type uploadIn struct {
	Body struct {
		Data string `json:"data"`
	}
}

// jsonBody — JSON-тело ровно n байт.
func jsonBody(n int) io.Reader {
	return strings.NewReader(`{"data":"` + strings.Repeat("a", n-len(`{"data":""}`)) + `"}`)
}

func apiErrorOf(t *testing.T, resp *http.Response) httpkit.APIError {
	t.Helper()
	var e httpkit.APIError
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	return e
}

// Регрессия KCOR-09 (H17): лимит операции выше HTTP_MAX_BODY_BYTES поднимает и лимит транспорта,
// скрытая операция живёт по тому же лимиту, что видимая, превышение — 413, а не 500.
func TestModule_OperationBodyLimit(t *testing.T) {
	t.Setenv("HTTP_MAX_BODY_BYTES", "1024")
	ok := func(context.Context, *uploadIn) (*struct{}, error) { return &struct{}{}, nil }
	srv := startApp(t,
		fx.Supply(httpkit.HumaConfigs{func(c *huma.Config) {
			c.OnAddOperation = append(c.OnAddOperation, func(_ *huma.OpenAPI, op *huma.Operation) {
				if op.OperationID == "upload" {
					op.MaxBodyBytes = 1 << 20
				}
			})
		}}),
		fx.Invoke(func(api huma.API) {
			huma.Register(api, huma.Operation{OperationID: "upload", Method: http.MethodPost, Path: "/upload"}, ok)
			huma.Register(api, huma.Operation{OperationID: "hidden", Method: http.MethodPost, Path: "/hidden", Hidden: true}, ok)
			huma.Register(api, huma.Operation{OperationID: "plain", Method: http.MethodPost, Path: "/plain"}, ok)
		}))
	ct := "Content-Type: application/json"

	if resp := do(t, srv, http.MethodPost, "/upload", jsonBody(4096), ct); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("raised operation limit, 4 KiB: %d %s", resp.StatusCode, bodyString(t, resp))
	}
	for _, path := range []string{"/hidden", "/plain"} {
		resp := do(t, srv, http.MethodPost, path, jsonBody(4096), ct)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s, 4 KiB over 1024: %d, want 413", path, resp.StatusCode)
		}
		if e := apiErrorOf(t, resp); e.Code != "BODY_TOO_LARGE" {
			t.Fatalf("%s: code %q, want BODY_TOO_LARGE", path, e.Code)
		}
	}
	if resp := do(t, srv, http.MethodPost, "/plain", jsonBody(512), ct); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("plain, 512 B: %d", resp.StatusCode)
	}
}

// Регрессия KSEC-13 (H04): 401 несёт WWW-Authenticate по схемам операции — http-схема своим
// именем, apiKey и схема вне components — именем схемы; другой отказ — без него.
func TestModule_WWWAuthenticateOn401(t *testing.T) {
	extract := httpkit.AuthExtractor(func(r *http.Request) (context.Context, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer ok":
			return r.Context(), nil
		case "down":
			return nil, httpkit.ErrAuthUnavailable
		}
		return nil, errors.New("no credentials")
	})
	op := func(id, path string, security ...map[string][]string) huma.Operation {
		return huma.Operation{OperationID: id, Method: http.MethodGet, Path: path, Security: security}
	}
	srv := startApp(t,
		httpkit.ProvideSecurityScheme(func() []httpkit.SecuritySchemeEntry {
			return []httpkit.SecuritySchemeEntry{
				{Name: "bearerAuth", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"}, Extract: extract},
				{Name: "AdminKey", Scheme: &huma.SecurityScheme{Type: "apiKey", In: "header", Name: "X-Admin-Key"}, Extract: extract},
				{Name: "oidc", Scheme: &huma.SecurityScheme{Type: "openIdConnect", OpenIDConnectURL: "https://id.example.com/.well-known/openid-configuration"}, Extract: extract},
			}
		}),
		fx.Invoke(func(api huma.API) {
			noop := func(context.Context, *struct{}) (*struct{}, error) { return &struct{}{}, nil }
			huma.Register(api, op("bearer", "/bearer", map[string][]string{"bearerAuth": {}}), noop)
			huma.Register(api, op("admin", "/admin", map[string][]string{"AdminKey": {}}), noop)
			huma.Register(api, op("either", "/either", map[string][]string{"bearerAuth": {}}, map[string][]string{"AdminKey": {}}), noop)
			huma.Register(api, op("same-scheme", "/same", map[string][]string{"bearerAuth": {}}, map[string][]string{"oidc": {}}), noop)
			huma.Register(api, httpkit.Public(huma.Operation{OperationID: "health-like", Method: http.MethodGet, Path: "/open"}), noop)
		}))

	for path, want := range map[string]string{"/bearer": "Bearer", "/admin": "AdminKey", "/either": "Bearer, AdminKey", "/same": "Bearer"} {
		resp := get(t, srv, path)
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != want {
			t.Errorf("GET %s: %d, WWW-Authenticate %q; want 401 and %q", path, resp.StatusCode, resp.Header.Get("WWW-Authenticate"), want)
		}
	}
	resp := do(t, srv, http.MethodGet, "/bearer", nil, "Authorization: down")
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("WWW-Authenticate") != "" {
		t.Errorf("auth backend down: %d, WWW-Authenticate %q; want 503 without a challenge", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

// HumaAuth на API без хука кита (humatest потребителя) берёт вызов из имён схем операции.
func TestHumaAuth_ChallengeWithoutKitHook(t *testing.T) {
	_, api := humatest.New(t)
	api.UseMiddleware(httpkit.HumaAuth([]httpkit.SecuritySchemeEntry{{Name: "Bearer", Extract: func(*http.Request) (context.Context, error) {
		return nil, errors.New("no credentials")
	}}}, logkit.Nop()))
	huma.Register(api, huma.Operation{
		OperationID: "secure", Method: http.MethodGet, Path: "/secure",
		Security: []map[string][]string{{"Bearer": {}}},
	}, func(context.Context, *struct{}) (*struct{}, error) { return &struct{}{}, nil })

	resp := api.Get("/secure")
	if resp.Code != http.StatusUnauthorized || resp.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("401 = %d, WWW-Authenticate %q; want Bearer", resp.Code, resp.Header().Get("WWW-Authenticate"))
	}
}

// Регрессия KSEC-13 (H07): HEAD на GET-маршрутах, /health и /ready тоже, — как GET без тела; у
// маршрута без GET — 405.
func TestModule_HeadOnGetRoutes(t *testing.T) {
	srv := startApp(t, fx.Invoke(func(api huma.API) {
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "item", Method: http.MethodGet, Path: "/items/{id}"}),
			func(context.Context, *struct {
				ID string `path:"id"`
			},
			) (*struct{ Body map[string]string }, error) {
				return &struct{ Body map[string]string }{Body: map[string]string{"id": "1"}}, nil
			})
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "create", Method: http.MethodPost, Path: "/create"}),
			func(context.Context, *struct{}) (*struct{}, error) { return &struct{}{}, nil })
	}))

	for _, path := range []string{"/health", "/ready", "/items/1"} {
		resp := do(t, srv, http.MethodHead, path, nil)
		if resp.StatusCode != http.StatusOK || bodyString(t, resp) != "" {
			t.Errorf("HEAD %s: %d, want 200 without a body", path, resp.StatusCode)
		}
	}
	resp := do(t, srv, http.MethodHead, "/create", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed || !slices.Equal(resp.Header.Values("Allow"), []string{http.MethodPost}) {
		t.Errorf("HEAD on a POST route: %d, Allow %v; want 405 and [POST]", resp.StatusCode, resp.Header.Values("Allow"))
	}
}

// Регрессия KSEC-15 (H35): хендлер дольше HTTP_REQUEST_TIMEOUT — у ctx дедлайн, клиент получает
// 503 problem+json к дедлайну, а не обрыв по WriteTimeout; поток SSE идёт без дедлайна.
func TestModule_RequestDeadline(t *testing.T) {
	t.Setenv("HTTP_WRITE_TIMEOUT", "2s")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "200ms")
	var hasDeadline atomic.Bool
	srv := startApp(t, fx.Invoke(func(api huma.API) {
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "slow", Method: http.MethodGet, Path: "/slow"}),
			func(ctx context.Context, _ *struct{}) (*struct{}, error) {
				_, ok := ctx.Deadline()
				hasDeadline.Store(ok)
				time.Sleep(600 * time.Millisecond)
				return &struct{}{}, nil
			})
	}))

	start := time.Now()
	resp := get(t, srv, "/slow")
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Content-Type") != httpkit.ProblemContentType {
		t.Fatalf("slow handler: %d %q, want 503 problem+json", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if e := apiErrorOf(t, resp); e.Code != "REQUEST_TIMEOUT" || e.RequestID == "" {
		t.Fatalf("problem: %+v, want code REQUEST_TIMEOUT with request_id", e)
	}
	if elapsed >= 550*time.Millisecond {
		t.Errorf("503 arrived after %s: it must not wait for the handler", elapsed)
	}
	if !hasDeadline.Load() {
		t.Error("the handler ctx has no deadline")
	}

	resp = do(t, srv, http.MethodGet, "/slow", nil, "Accept: text/event-stream")
	if resp.StatusCode != http.StatusNoContent || hasDeadline.Load() {
		t.Errorf("SSE request: %d, ctx deadline %v; want the handler's 204 without a deadline", resp.StatusCode, hasDeadline.Load())
	}
}

// Дедлайн пишет 503 только за хендлер, не начавший ответ: начатый ответ дописывается как есть, а
// запись после 503 хендлеру возвращает http.ErrHandlerTimeout.
func TestModule_RequestDeadlineKeepsStartedResponse(t *testing.T) {
	t.Setenv("HTTP_WRITE_TIMEOUT", "2s")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "100ms")
	late := make(chan error, 1)
	srv := startApp(t, fx.Invoke(func(_ huma.API, s *httpkit.Server) {
		s.Router().Get("/started", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Handler", "yes")
			_, _ = w.Write([]byte("partial "))
			_ = http.NewResponseController(w).Flush()
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte("done"))
		})
		s.Router().Get("/late", func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(300 * time.Millisecond)
			_, err := w.Write([]byte("late"))
			late <- err
		})
	}))

	resp := get(t, srv, "/started")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Handler") != "yes" || bodyString(t, resp) != "partial done" {
		t.Fatalf("started response: %d %v", resp.StatusCode, resp.Header)
	}
	if resp := get(t, srv, "/late"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("late handler: %d, want 503", resp.StatusCode)
	}
	if err := <-late; !errors.Is(err, http.ErrHandlerTimeout) {
		t.Fatalf("write after the deadline: %v, want http.ErrHandlerTimeout", err)
	}
}

// Дедлайн — над Idempotency: она держит резерв, пока срезанный хендлер не вернётся, и сохраняет
// его ответ — повтор с тем же ключом получает результат, а не второе выполнение.
func TestModule_RequestDeadlineKeepsIdempotencyReservation(t *testing.T) {
	t.Setenv("HTTP_WRITE_TIMEOUT", "2s")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "100ms")
	var calls atomic.Int32
	done := make(chan struct{})
	srv := startApp(t,
		fx.Provide(func() httpkit.IdempotencyStore { return httpkit.NewMemoryStore() }),
		fx.Invoke(func(api huma.API) {
			huma.Register(api, huma.Operation{OperationID: "charge", Method: http.MethodPost, Path: "/charge", DefaultStatus: http.StatusCreated},
				func(context.Context, *struct{}) (*struct{}, error) {
					calls.Add(1)
					time.Sleep(300 * time.Millisecond)
					defer close(done)
					return &struct{}{}, nil
				})
		}))
	hdr := []string{"Idempotency-Key: charge-1", "Authorization: Bearer alice"}

	if resp := do(t, srv, http.MethodPost, "/charge", nil, hdr...); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("first: %d, want 503 at the deadline", resp.StatusCode)
	}
	if resp := do(t, srv, http.MethodPost, "/charge", nil, hdr...); resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry while the handler runs: %d, want 409 in progress", resp.StatusCode)
	}
	<-done
	var resp *http.Response
	for range 50 {
		if resp = do(t, srv, http.MethodPost, "/charge", nil, hdr...); resp.StatusCode != http.StatusConflict {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resp.StatusCode != http.StatusCreated || resp.Header.Get(httpkit.HeaderIdempotentReplayed) != "true" || calls.Load() != 1 {
		t.Fatalf("retry after the handler: %d replayed=%q calls=%d; want the stored 201 and one call",
			resp.StatusCode, resp.Header.Get(httpkit.HeaderIdempotentReplayed), calls.Load())
	}
}

func TestConfig_RequestTimeoutRules(t *testing.T) {
	base := testConfig(t, "127.0.0.1:0")
	cases := map[string]func(*httpkit.Config){
		"zero":                 func(c *httpkit.Config) { c.RequestTimeout = 0 },
		"not below write":      func(c *httpkit.Config) { c.RequestTimeout = c.WriteTimeout },
		"reserve within it":    func(c *httpkit.Config) { c.Idempotency.ReserveTTL = c.RequestTimeout },
		"reserve shorter than": func(c *httpkit.Config) { c.Idempotency.ReserveTTL = time.Second },
	}
	for name, mutate := range cases {
		cfg := base
		mutate(&cfg)
		if err := cfg.Validate(); !errors.Is(err, httpkit.ErrConfig) {
			t.Errorf("%s: %v, want ErrConfig", name, err)
		}
	}
	off := base
	off.RequestTimeout, off.Idempotency.ReserveTTL = -time.Second, time.Second
	if err := off.Validate(); err != nil {
		t.Errorf("negative request timeout (off): %v", err)
	}
	noWrite := base
	noWrite.WriteTimeout = 0
	if err := noWrite.Validate(); err != nil {
		t.Errorf("request timeout without write timeout: %v", err)
	}
}

// Регрессия KAPI-09 (H10): CORS по умолчанию пускает заголовок идемпотентности кита.
func TestModule_CORSAllowsIdempotencyKeyByDefault(t *testing.T) {
	t.Setenv("HTTP_CORS_ORIGINS", "https://app.example.com")
	srv := startApp(t)
	resp := do(t, srv, http.MethodOptions, "/health", nil, "Origin: https://app.example.com",
		"Access-Control-Request-Method: POST", "Access-Control-Request-Headers: content-type, idempotency-key")
	if acah := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(acah, "Idempotency-Key") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want Idempotency-Key", acah)
	}
}

// Регрессия KCOR-11 (H06): panic после записи заголовка обрывает ответ — клиент получает начатый
// 200 и ошибку чтения тела, а не полный «успешный» ответ; panic — в логе.
func TestModule_PanicAfterHeaderAbortsResponse(t *testing.T) {
	log := logtest.Capture()
	var srv *httpkit.Server
	app := fx.New(fx.NopLogger,
		fx.Supply(testConfig(t, "127.0.0.1:0")),
		fx.Provide(func() logkit.Logger { return log }),
		httpkit.Module, fx.Populate(&srv),
		fx.Invoke(func(api huma.API) {
			huma.Get(api, "/panic-after-write", func(context.Context, *struct{}) (*huma.StreamResponse, error) {
				return &huma.StreamResponse{Body: func(ctx huma.Context) {
					ctx.SetStatus(http.StatusOK)
					_, _ = ctx.BodyWriter().Write([]byte("partial"))
					panic("late")
				}}, nil
			})
		}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	resp := get(t, srv, "/panic-after-write")
	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || err == nil {
		t.Fatalf("status %d, body %q, read error %v; want the started 200 cut off", resp.StatusCode, body, err)
	}
	if !log.Has(slog.LevelError, "panic recovered") {
		t.Fatal("panic after the header is not logged")
	}
}

// Recovery без записанного заголовка отвечает 500, после него — обрывает ответ ErrAbortHandler.
func TestRecovery_PanicAfterHeaderIsAbort(t *testing.T) {
	h := httpkit.Recovery(logkit.Nop())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic("late")
	}))
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler { //nolint:errorlint // panic-значение сравнивается как есть
			t.Fatalf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

// Регрессия KSEC-08 (H15): операцию открывает только её схема — extractor каждой схемы
// альтернативы, альтернатива целиком; пустая альтернатива открывает без credentials.
func TestModule_AuthOpensOnlyByOperationScheme(t *testing.T) {
	user := func(r *http.Request) (context.Context, error) {
		if r.Header.Get("Authorization") == "Bearer user" {
			return httpkit.ContextWithSubject(r.Context(), "user"), nil
		}
		return nil, errors.New("no bearer")
	}
	admin := func(r *http.Request) (context.Context, error) {
		if r.Header.Get("X-Admin-Key") == "k" {
			return r.Context(), nil
		}
		return nil, errors.New("no admin key")
	}
	srv := startApp(t,
		httpkit.ProvideSecurityScheme(func() []httpkit.SecuritySchemeEntry {
			return []httpkit.SecuritySchemeEntry{
				{Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"}, Extract: user},
				{Name: "AdminKey", Scheme: &huma.SecurityScheme{Type: "apiKey", In: "header", Name: "X-Admin-Key"}, Extract: admin},
			}
		}),
		fx.Invoke(func(api huma.API) {
			reg := func(id, path string, security ...map[string][]string) {
				huma.Register(api, huma.Operation{OperationID: id, Method: http.MethodPost, Path: path, Security: security},
					func(context.Context, *struct{}) (*struct{}, error) { return &struct{}{}, nil })
			}
			reg("wipe", "/admin/wipe", map[string][]string{"AdminKey": {}})
			reg("either", "/either", map[string][]string{"Bearer": {}}, map[string][]string{"AdminKey": {}})
			reg("both", "/both", map[string][]string{"Bearer": {}, "AdminKey": {}})
			reg("optional", "/optional", map[string][]string{"Bearer": {}}, map[string][]string{})
		}))
	bearer, key := "Authorization: Bearer user", "X-Admin-Key: k"
	for _, c := range []struct {
		path    string
		headers []string
		want    int
	}{
		{"/admin/wipe", []string{bearer}, http.StatusUnauthorized},
		{"/admin/wipe", []string{key}, http.StatusNoContent},
		{"/either", []string{bearer}, http.StatusNoContent},
		{"/either", []string{key}, http.StatusNoContent},
		{"/both", []string{bearer}, http.StatusUnauthorized},
		{"/both", []string{bearer, key}, http.StatusNoContent},
		{"/optional", nil, http.StatusNoContent},
	} {
		if resp := do(t, srv, http.MethodPost, c.path, nil, c.headers...); resp.StatusCode != c.want {
			t.Errorf("POST %s %v: %d, want %d", c.path, c.headers, resp.StatusCode, c.want)
		}
	}
}

// Регрессия N8: схема Security вне components — ошибка старта и без схем в графе.
func TestModule_SecurityOutsideComponentsFailsStart(t *testing.T) {
	err := startErr(t, register(huma.Operation{OperationID: "me", Method: http.MethodGet, Path: "/me", Security: bearerOnly}))
	if !errors.Is(err, httpkit.ErrRoutes) || !strings.Contains(err.Error(), `"Bearer" missing from components`) {
		t.Fatalf("start error = %v, want ErrRoutes naming the missing scheme", err)
	}
}

// Регрессия N7 (m6-04): схема в components через HumaConfigs тоже требует у операции Security
// или Public, а без extractor'а её операция не стартует.
func TestModule_SchemeFromHumaConfigsGuards(t *testing.T) {
	cookie := fx.Supply(httpkit.HumaConfigs{func(c *huma.Config) {
		c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{"Cookie": {Type: "apiKey", In: "cookie", Name: "sid"}}
	}})
	err := startErr(t, cookie, register(huma.Operation{OperationID: "list", Method: http.MethodGet, Path: "/items"}))
	if !errors.Is(err, httpkit.ErrRoutes) || !strings.Contains(err.Error(), "list has no Security") {
		t.Fatalf("start error = %v, want ErrRoutes for the operation without Security", err)
	}
	err = startErr(t, cookie, register(huma.Operation{
		OperationID: "me", Method: http.MethodGet, Path: "/me",
		Security: []map[string][]string{{"Cookie": {}}},
	}))
	if !errors.Is(err, httpkit.ErrAuthNotConfigured) || !strings.Contains(err.Error(), "me (Cookie)") {
		t.Fatalf("start error = %v, want ErrAuthNotConfigured for a scheme without an extractor", err)
	}
}

// Регрессия KFX-11 (HX07), KOBS-12: Idempotency — после HumaAuth. Extractor зовётся один раз и при
// отказе бэкенда, отозванная сессия получает 401, а не replay, ротация токена того же субъекта —
// replay, и он учтён по маршруту.
func TestModule_IdempotencyAfterAuth(t *testing.T) {
	var calls, extracts atomic.Int32
	extract := func(r *http.Request) (context.Context, error) {
		extracts.Add(1)
		switch r.Header.Get("Authorization") {
		case "Bearer old", "Bearer new":
			return httpkit.ContextWithSubject(r.Context(), "user-1"), nil
		case "Bearer down":
			return nil, errors.Join(errors.New("jwks"), httpkit.ErrAuthUnavailable)
		}
		return nil, errors.New("revoked")
	}
	srv := startApp(t,
		httpkit.ProvideSecurityScheme(func() httpkit.SecuritySchemeEntry {
			return httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"}, Extract: extract}
		}),
		fx.Provide(func() httpkit.IdempotencyStore { return httpkit.NewMemoryStore() }),
		fx.Invoke(func(api huma.API) {
			huma.Register(api, huma.Operation{
				OperationID: "create-order", Method: http.MethodPost, Path: "/orders",
				DefaultStatus: http.StatusCreated, Security: bearerOnly,
			},
				func(context.Context, *struct{}) (*struct{}, error) { calls.Add(1); return &struct{}{}, nil })
		}))
	key := "Idempotency-Key: k-1"

	if resp := do(t, srv, http.MethodPost, "/orders", nil, "Authorization: Bearer old", key); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first: %d", resp.StatusCode)
	}
	if resp := do(t, srv, http.MethodPost, "/orders", nil, "Authorization: Bearer revoked", key); resp.StatusCode != http.StatusUnauthorized ||
		resp.Header.Get(httpkit.HeaderIdempotentReplayed) != "" {
		t.Fatalf("revoked session: %d replayed=%q, want 401 without a replay", resp.StatusCode, resp.Header.Get(httpkit.HeaderIdempotentReplayed))
	}
	before := metrictest.Collect(t)
	resp := do(t, srv, http.MethodPost, "/orders", nil, "Authorization: Bearer new", key)
	if resp.StatusCode != http.StatusCreated || resp.Header.Get(httpkit.HeaderIdempotentReplayed) != "true" || calls.Load() != 1 {
		t.Fatalf("rotated token: %d replayed=%q calls=%d, want the replay", resp.StatusCode, resp.Header.Get(httpkit.HeaderIdempotentReplayed), calls.Load())
	}
	if n := metrictest.Collect(t).Sub(before).Value("http_requests_total", "path", "/orders", "status", "201"); n != 1 {
		t.Fatalf("replay counted under the route %v times, want 1", n)
	}
	extracts.Store(0)
	if resp := do(t, srv, http.MethodPost, "/orders", nil, "Authorization: Bearer down", key); resp.StatusCode != http.StatusServiceUnavailable || extracts.Load() != 1 {
		t.Fatalf("auth backend down: %d, extractor calls %d; want 503 and one call", resp.StatusCode, extracts.Load())
	}
}
