package httpkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// маршрута без GET — 405; свой HEAD маршрута остаётся его.
func TestModule_HeadOnGetRoutes(t *testing.T) {
	type headOut struct {
		Own string `header:"X-Head"`
	}
	srv := startApp(t, fx.Invoke(func(api huma.API) {
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "file", Method: http.MethodGet, Path: "/files/{id}"}),
			func(context.Context, *struct{}) (*struct{ Body string }, error) {
				return &struct{ Body string }{Body: "file"}, nil
			})
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "file-head", Method: http.MethodHead, Path: "/files/{id}"}),
			func(context.Context, *struct{}) (*headOut, error) { return &headOut{Own: "own"}, nil })
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
	if resp := do(t, srv, http.MethodHead, "/files/1", nil); resp.Header.Get("X-Head") != "own" {
		t.Errorf("HEAD on a route with its own HEAD: %d %v, want its own handler", resp.StatusCode, resp.Header)
	}
}

// Регрессия KSEC-15 (H35): хендлер дольше HTTP_REQUEST_TIMEOUT — у ctx дедлайн, клиент получает
// 503 problem+json к дедлайну, а не обрыв по WriteTimeout; поток SSE (GET) идёт без дедлайна, а
// Accept: text/event-stream на записи дедлайна не снимает.
func TestModule_RequestDeadline(t *testing.T) {
	t.Setenv("HTTP_WRITE_TIMEOUT", "2s")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "200ms")
	var hasDeadline atomic.Bool
	slow := func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		_, ok := ctx.Deadline()
		hasDeadline.Store(ok)
		time.Sleep(600 * time.Millisecond)
		return &struct{}{}, nil
	}
	srv := startApp(t, fx.Invoke(func(api huma.API) {
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "slow", Method: http.MethodGet, Path: "/slow"}), slow)
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "slow-write", Method: http.MethodPost, Path: "/slow"}), slow)
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
	for _, h := range []string{"Accept: text/event-stream", "Upgrade: websocket"} {
		if resp := do(t, srv, http.MethodPost, "/slow", nil, h); resp.StatusCode != http.StatusServiceUnavailable || !hasDeadline.Load() {
			t.Errorf("POST with %q: %d, ctx deadline %v; want 503 with a deadline", h, resp.StatusCode, hasDeadline.Load())
		}
	}
}

// Дедлайн пишет 503 только за хендлер, не начавший ответ: начатый ответ дописывается как есть, а
// запись после 503 молча отбрасывается — без ошибки, на которой паникует huma.
func TestModule_RequestDeadlineKeepsStartedResponse(t *testing.T) {
	t.Setenv("HTTP_WRITE_TIMEOUT", "2s")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "100ms")
	type write struct {
		n   int
		err error
	}
	late := make(chan write, 1)
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
			n, err := w.Write([]byte("late"))
			late <- write{n, err}
		})
	}))

	resp := get(t, srv, "/started")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Handler") != "yes" || bodyString(t, resp) != "partial done" {
		t.Fatalf("started response: %d %v", resp.StatusCode, resp.Header)
	}
	resp = get(t, srv, "/late")
	if e := apiErrorOf(t, resp); resp.StatusCode != http.StatusServiceUnavailable || e.Code != "REQUEST_TIMEOUT" {
		t.Fatalf("late handler: %d %+v, want 503 REQUEST_TIMEOUT", resp.StatusCode, e)
	}
	if w := <-late; w.n != len("late") || w.err != nil {
		t.Fatalf("write after the deadline: %d, %v; want it dropped without an error", w.n, w.err)
	}
}

// 503 дедлайна — целый ответ (Content-Length, Connection: close): клиент дочитывает его, пока
// хендлер ещё работает, — и когда хендлер переживает WriteTimeout, — а не ждёт конца ответа.
func TestModule_RequestDeadlineResponseIsComplete(t *testing.T) {
	t.Setenv("HTTP_WRITE_TIMEOUT", "2s")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "100ms")
	release := make(chan struct{})
	srv := startApp(t, fx.Invoke(func(api huma.API) {
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "stuck", Method: http.MethodGet, Path: "/stuck"}),
			func(context.Context, *struct{}) (*struct{}, error) {
				<-release
				return &struct{}{}, nil
			})
	}))
	t.Cleanup(func() { close(release) })

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + srv.Addr().String() + "/stuck")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"REQUEST_TIMEOUT"`) {
		t.Fatalf("503 body read while the handler runs: %d %q %v; want the whole problem+json", resp.StatusCode, body, err)
	}
	if resp.ContentLength != int64(len(body)) || !resp.Close {
		t.Fatalf("Content-Length %d for %d bytes, Connection: close %v; want a self-delimited response on a closing connection",
			resp.ContentLength, len(body), resp.Close)
	}
}

// Дедлайн — над Idempotency: она держит резерв, пока срезанный хендлер не вернётся, и сохраняет
// его ответ с телом — повтор с тем же ключом получает результат, а не второе выполнение; паники
// на записи после 503 нет.
func TestModule_RequestDeadlineKeepsIdempotencyReservation(t *testing.T) {
	t.Setenv("HTTP_WRITE_TIMEOUT", "2s")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "100ms")
	log := logtest.Capture()
	before := metrictest.Collect(t)
	var calls atomic.Int32
	done := make(chan struct{})
	srv := startApp(t,
		fx.Decorate(func(logkit.Logger) logkit.Logger { return log }), withBearer,
		fx.Provide(func() httpkit.IdempotencyStore { return httpkit.NewMemoryStore() }),
		fx.Invoke(func(api huma.API) {
			huma.Register(api, huma.Operation{OperationID: "charge", Method: http.MethodPost, Path: "/charge", DefaultStatus: http.StatusCreated, Security: bearerOnly},
				func(context.Context, *struct{}) (*struct{ Body map[string]string }, error) {
					calls.Add(1)
					time.Sleep(300 * time.Millisecond)
					defer close(done)
					return &struct{ Body map[string]string }{Body: map[string]string{"charge": "ch-1"}}, nil
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
	if body := bodyString(t, resp); resp.StatusCode != http.StatusCreated || resp.Header.Get(httpkit.HeaderIdempotentReplayed) != "true" ||
		!strings.Contains(body, `"ch-1"`) || calls.Load() != 1 {
		t.Fatalf("retry after the handler: %d replayed=%q %s calls=%d; want the stored 201 with its body and one call",
			resp.StatusCode, resp.Header.Get(httpkit.HeaderIdempotentReplayed), body, calls.Load())
	}
	if log.Has(slog.LevelError, "panic recovered") || len(metrictest.Collect(t).Sub(before).Find("http_panics_total")) > 0 {
		t.Fatalf("a write after the deadline panicked: %v", log.Entries())
	}
}

// Ошибка хендлера после дедлайна — не паника: ошибка ctx и APIError доходят до huma без сбоя записи;
// отказ < 500 Idempotency сохраняет — повтор с ключом его воспроизводит, а не выполняет заново.
func TestModule_RequestDeadlineLateErrorIsNotAPanic(t *testing.T) {
	t.Setenv("HTTP_WRITE_TIMEOUT", "2s")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "100ms")
	log := logtest.Capture()
	before := metrictest.Collect(t)
	var calls atomic.Int32
	srv := startApp(t,
		fx.Decorate(func(logkit.Logger) logkit.Logger { return log }), withBearer,
		fx.Provide(func() httpkit.IdempotencyStore { return httpkit.NewMemoryStore() }),
		fx.Invoke(func(api huma.API) {
			huma.Register(api, httpkit.Public(huma.Operation{OperationID: "query", Method: http.MethodGet, Path: "/query"}),
				func(ctx context.Context, _ *struct{}) (*struct{ Body string }, error) {
					<-ctx.Done()
					return nil, fmt.Errorf("db: %w", ctx.Err())
				})
			huma.Register(api, huma.Operation{OperationID: "reserve", Method: http.MethodPost, Path: "/reserve", Security: bearerOnly},
				func(ctx context.Context, _ *struct{}) (*struct{ Body string }, error) {
					calls.Add(1)
					<-ctx.Done()
					return nil, httpkit.Error("SOLD_OUT", http.StatusGone, "sold out")
				})
		}))
	served := func(path string) {
		t.Helper()
		for range 100 {
			if _, ok := log.FindFunc(func(e logtest.Entry) bool { return e.Msg == "http" && e.Str("path") == path }); ok {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no access-log line for %s", path)
	}

	if resp := get(t, srv, "/query"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ctx error after the deadline: %d, want the 503 of the deadline", resp.StatusCode)
	}
	served("/query")
	hdr := []string{"Idempotency-Key: r-1", "Authorization: Bearer alice"}
	if resp := do(t, srv, http.MethodPost, "/reserve", nil, hdr...); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("APIError after the deadline: %d, want the 503 of the deadline", resp.StatusCode)
	}
	served("/reserve")
	if resp := do(t, srv, http.MethodPost, "/reserve", nil, hdr...); resp.StatusCode != http.StatusGone ||
		resp.Header.Get(httpkit.HeaderIdempotentReplayed) != "true" || calls.Load() != 1 {
		t.Fatalf("retry: %d replayed=%q calls=%d; want the stored 410 and one call",
			resp.StatusCode, resp.Header.Get(httpkit.HeaderIdempotentReplayed), calls.Load())
	}
	if log.Has(slog.LevelError, "panic recovered") || len(metrictest.Collect(t).Sub(before).Find("http_panics_total")) > 0 {
		t.Fatalf("an error after the deadline panicked: %v", log.Entries())
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
// альтернативы, альтернатива целиком, ctx схемы — следующей схеме альтернативы; отказ бэкенда
// любой альтернативы — 503. Пустая альтернатива открывает без credentials и идёт последней:
// валидный токен даёт личность, где бы `{}` ни стоял, а отказ бэкенда — 503, не аноним.
func TestModule_AuthOpensOnlyByOperationScheme(t *testing.T) {
	type roleKey struct{}
	user := func(r *http.Request) (context.Context, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer user":
			return httpkit.ContextWithSubject(r.Context(), "user"), nil
		case "Bearer down":
			return nil, errors.Join(errors.New("jwks"), httpkit.ErrAuthUnavailable)
		}
		return nil, errors.New("no bearer")
	}
	admin := func(r *http.Request) (context.Context, error) {
		switch r.Header.Get("X-Admin-Key") {
		case "k":
			return context.WithValue(r.Context(), roleKey{}, "admin"), nil
		case "down":
			return nil, errors.Join(errors.New("key store"), httpkit.ErrAuthUnavailable)
		}
		return nil, errors.New("no admin key")
	}
	type identity struct {
		Subject string `header:"X-Subject"`
		Role    string `header:"X-Role"`
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
					func(ctx context.Context, _ *struct{}) (*identity, error) {
						role, _ := ctx.Value(roleKey{}).(string)
						return &identity{Subject: httpkit.SubjectFrom(ctx), Role: role}, nil
					})
			}
			reg("wipe", "/admin/wipe", map[string][]string{"AdminKey": {}})
			reg("either", "/either", map[string][]string{"Bearer": {}}, map[string][]string{"AdminKey": {}})
			reg("both", "/both", map[string][]string{"Bearer": {}, "AdminKey": {}})
			reg("optional", "/optional", map[string][]string{"Bearer": {}}, map[string][]string{})
			reg("optional-first", "/optional-first", map[string][]string{}, map[string][]string{"Bearer": {}})
		}))
	bearer, key := "Authorization: Bearer user", "X-Admin-Key: k"
	for _, c := range []struct {
		path          string
		headers       []string
		want          int
		subject, role string
	}{
		{"/admin/wipe", []string{bearer}, http.StatusUnauthorized, "", ""},
		{"/admin/wipe", []string{key}, http.StatusNoContent, "", "admin"},
		{"/either", []string{bearer}, http.StatusNoContent, "user", ""},
		{"/either", []string{key}, http.StatusNoContent, "", "admin"},
		{"/either", []string{"X-Admin-Key: down"}, http.StatusServiceUnavailable, "", ""},
		{"/both", []string{bearer}, http.StatusUnauthorized, "", ""},
		{"/both", []string{bearer, key}, http.StatusNoContent, "user", "admin"},
		{"/optional", nil, http.StatusNoContent, "", ""},
		{"/optional", []string{"Authorization: Bearer bad"}, http.StatusNoContent, "", ""},
		{"/optional", []string{bearer}, http.StatusNoContent, "user", ""},
		{"/optional", []string{"Authorization: Bearer down"}, http.StatusServiceUnavailable, "", ""},
		{"/optional-first", []string{bearer}, http.StatusNoContent, "user", ""},
		{"/optional-first", nil, http.StatusNoContent, "", ""},
	} {
		resp := do(t, srv, http.MethodPost, c.path, nil, c.headers...)
		if resp.StatusCode != c.want || resp.Header.Get("X-Subject") != c.subject || resp.Header.Get("X-Role") != c.role {
			t.Errorf("POST %s %v: %d subject=%q role=%q, want %d subject=%q role=%q", c.path, c.headers,
				resp.StatusCode, resp.Header.Get("X-Subject"), resp.Header.Get("X-Role"), c.want, c.subject, c.role)
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
