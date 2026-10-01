package httpkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func testConfig(t *testing.T, addr string) httpkit.Config {
	t.Helper()
	t.Setenv("HTTP_ADDR", addr)
	cfg, err := httpkit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	return cfg
}

func startApp(t *testing.T, opts ...fx.Option) *httpkit.Server {
	t.Helper()
	var srv *httpkit.Server
	app := fx.New(append([]fx.Option{
		fx.NopLogger,
		fx.Supply(testConfig(t, "127.0.0.1:0")),
		fx.Provide(logkit.Nop),
		httpkit.Module,
		fx.Populate(&srv),
	}, opts...)...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = app.Stop(stopCtx)
	})
	return srv
}

func startAppWithProbe(t *testing.T, probe healthkit.Probe) *httpkit.Server {
	t.Helper()
	return startApp(t, healthkit.Provide(func() healthkit.Probe { return probe }))
}

func do(t *testing.T, srv *httpkit.Server, method, path string, body io.Reader, headers ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, "http://"+srv.Addr().String()+path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ":")
		req.Header.Set(name, strings.TrimSpace(value))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func get(t *testing.T, srv *httpkit.Server, path string) *http.Response {
	t.Helper()
	return do(t, srv, http.MethodGet, path, nil)
}

func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// Регрессия: пробы китов из группы `healthkit.probes` должны доходить до /ready.
func TestModule_ReadyReflectsProbes(t *testing.T) {
	down := healthkit.New("pgkit", func(context.Context) error { return errors.New("postgres: connection refused") })
	srv := startAppWithProbe(t, down)

	if resp := get(t, srv, "/health"); resp.StatusCode != http.StatusOK {
		t.Fatalf("/health: %d", resp.StatusCode)
	}
	resp := get(t, srv, "/ready")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/ready with failing probe: want 503, got %d", resp.StatusCode)
	}
	if resp.Header.Get(httpkit.HeaderRequestID) == "" {
		t.Error("response must carry X-Request-ID")
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("security headers must be applied")
	}

	var body struct {
		Status string `json:"status"`
		Probes []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"probes"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &body); err != nil {
		t.Fatalf("decode /ready body: %v", err)
	}
	if body.Status != "unhealthy" || len(body.Probes) != 1 {
		t.Fatalf("/ready body: %+v", body)
	}
	if body.Probes[0].Name != "pgkit" || body.Probes[0].Status != "down" {
		t.Errorf("/ready must name the failing dependency: %+v", body.Probes[0])
	}

	ok := healthkit.New("pgkit", func(context.Context) error { return nil })
	srv2 := startAppWithProbe(t, ok)
	if resp := get(t, srv2, "/ready"); resp.StatusCode != http.StatusOK {
		t.Fatalf("/ready with ok probe: want 200, got %d", resp.StatusCode)
	}
}

func TestModule_ReadyDoesNotLeakProbeError(t *testing.T) {
	down := healthkit.New("pgkit", func(context.Context) error {
		return errors.New("postgres://user:secret@10.0.0.5:5432 refused")
	})
	srv := startAppWithProbe(t, down)
	body := bodyString(t, get(t, srv, "/ready"))
	if strings.Contains(body, "secret") || strings.Contains(body, "10.0.0.5") {
		t.Fatalf("probe error leaked into /ready body: %s", body)
	}
}

// Регрессия: зависшая проба не должна держать /ready дольше своего таймаута —
// латентность readiness ограничена максимумом, а не суммой проб.
func TestModule_ReadyProbeTimeout(t *testing.T) {
	t.Setenv("HTTP_READY_PROBE_TIMEOUT", "100ms")
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	hang := healthkit.New("natskit", func(ctx context.Context) error {
		select {
		case <-blocked:
		case <-ctx.Done():
		}
		return ctx.Err()
	})

	srv := startApp(t,
		healthkit.Provide(func() healthkit.Probe { return hang }),
		healthkit.Provide(func() healthkit.Probe {
			return healthkit.New("pgkit", func(context.Context) error { return nil })
		}),
	)

	start := time.Now()
	resp := get(t, srv, "/ready")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("hanging probe must fail readiness: %d", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("/ready must not wait longer than the probe timeout: %s", elapsed)
	}
	if body := bodyString(t, resp); !strings.Contains(body, `"natskit"`) || !strings.Contains(body, `"pgkit"`) {
		t.Fatalf("/ready must list every probe: %s", body)
	}
}

func TestModule_StartFailsOnBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	app := fx.New(
		fx.NopLogger,
		fx.Supply(testConfig(t, ln.Addr().String())),
		fx.Provide(logkit.Nop),
		httpkit.Module,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err == nil {
		_ = app.Stop(ctx)
		t.Fatal("start on a busy port must fail synchronously")
	}
}

type boomInput struct {
	Body struct {
		Name string `json:"name"`
	}
}

// Регрессия (P0): незамапленная ошибка хендлера не уходит клиенту текстом и
// не теряется — единственная копия причины 500 должна быть в логе.
func TestModule_UnmappedHandlerErrorIsNeutralAndLogged(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	srv := startApp(t,
		fx.Decorate(func(logkit.Logger) logkit.Logger { return log }),
		fx.Invoke(func(api huma.API) {
			huma.Get(api, "/boom", func(context.Context, *struct{}) (*struct{}, error) {
				return nil, errors.New("pq: dial tcp 10.0.0.5:5432 user=admin password=s3cret")
			})
		}),
	)

	resp := do(t, srv, http.MethodGet, "/boom", nil, "X-Request-ID: req-boom")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if strings.Contains(body, "password") || strings.Contains(body, "10.0.0.5") {
		t.Fatalf("handler error text leaked to the client: %s", body)
	}
	if !strings.Contains(body, `"code":"INTERNAL"`) {
		t.Fatalf("500 body must carry a code: %s", body)
	}
	if !strings.Contains(buf.String(), "password=s3cret") {
		t.Fatalf("handler error must be logged: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "req-boom") {
		t.Fatalf("handler error log must carry request_id: %s", buf.String())
	}
}

// Регрессия: huma-валидация остаётся детальной — глушится только 5xx.
func TestModule_ValidationErrorKeepsDetails(t *testing.T) {
	srv := startApp(t, fx.Invoke(func(api huma.API) {
		huma.Register(api, huma.Operation{
			OperationID: "create", Method: http.MethodPost, Path: "/items",
		}, func(context.Context, *boomInput) (*struct{}, error) {
			return nil, nil //nolint:nilnil // тело ответа не нужно
		})
	}))

	resp := do(t, srv, http.MethodPost, "/items", strings.NewReader(`{"name":`), "Content-Type: application/json")
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("malformed body must be a 4xx, got %d", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "errors") {
		t.Fatalf("validation details must survive: %s", body)
	}
}

func TestModule_IdempotencyStoreWiring(t *testing.T) {
	calls := 0
	srv := startApp(t,
		fx.Provide(func() httpkit.IdempotencyStore { return httpkit.NewMemoryStore() }),
		fx.Invoke(func(api huma.API) {
			huma.Register(api, huma.Operation{
				OperationID: "create-order", Method: http.MethodPost, Path: "/orders",
				DefaultStatus: http.StatusCreated,
			}, func(context.Context, *struct{}) (*struct {
				Body struct {
					ID int `json:"id"`
				}
			}, error,
			) {
				calls++
				out := &struct {
					Body struct {
						ID int `json:"id"`
					}
				}{}
				out.Body.ID = calls
				return out, nil
			})
		}),
	)

	first := do(t, srv, http.MethodPost, "/orders", nil, "Idempotency-Key: k-1")
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first: %d", first.StatusCode)
	}
	second := do(t, srv, http.MethodPost, "/orders", nil, "Idempotency-Key: k-1")
	if second.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("second call must be replayed: %v", second.Header)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

// TestModule_Middlewares: middleware потребителя идут в порядке слайса httpkit.Middlewares
// (первый — внешний) внутри цепочки кита — с request-id в ctx и под Recovery.
func TestModule_Middlewares(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []string
	)
	mw := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if httpkit.RequestIDFrom(r.Context()) == "" {
					t.Errorf("middleware %s runs outside the kit chain: no request id", name)
				}
				if r.URL.Path == "/panic" && name == "last" {
					panic("consumer middleware")
				}
				mu.Lock()
				seen = append(seen, name)
				mu.Unlock()
				next.ServeHTTP(w, r)
			})
		}
	}
	order := []string{"first", "second", "third", "fourth", "last"}
	srv := startApp(t, fx.Provide(func() httpkit.Middlewares {
		mws := make(httpkit.Middlewares, 0, len(order))
		for _, name := range order {
			mws = append(mws, mw(name))
		}
		return mws
	}))

	for range 5 {
		mu.Lock()
		seen = nil
		mu.Unlock()
		if resp := get(t, srv, "/health"); resp.StatusCode != http.StatusOK {
			t.Fatalf("/health: %d", resp.StatusCode)
		}
		mu.Lock()
		got := slices.Clone(seen)
		mu.Unlock()
		if !slices.Equal(got, order) {
			t.Fatalf("middleware order = %v, want %v", got, order)
		}
	}

	resp := get(t, srv, "/panic")
	if resp.StatusCode != http.StatusInternalServerError || resp.Header.Get("Content-Type") != httpkit.ProblemContentType {
		t.Fatalf("panic in a consumer middleware must be recovered by the kit: %d %v", resp.StatusCode, resp.Header)
	}
}

// IP клиента — в ctx хендлера стандартной цепочкой: за HTTP_TRUST_PROXY_HOPS прокси — их запись
// X-Forwarded-For, левее — то, что прислал клиент.
func TestModule_ClientIPInContext(t *testing.T) {
	t.Setenv("HTTP_TRUST_PROXY_HOPS", "1")
	var got string
	srv := startApp(t, fx.Invoke(func(api huma.API) {
		huma.Get(api, "/whoami", func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			got = httpkit.ClientIPFrom(ctx)
			return nil, nil //nolint:nilnil // тело ответа не нужно
		})
	}))
	if resp := do(t, srv, http.MethodGet, "/whoami", nil, "X-Forwarded-For: 6.6.6.6, 203.0.113.7"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("/whoami: %d", resp.StatusCode)
	}
	if got != "203.0.113.7" {
		t.Fatalf("ClientIPFrom = %q, want the proxy-appended 203.0.113.7", got)
	}
}

// N6 валидатора: правки huma.Config потребителя идут в порядке слайса
// httpkit.HumaConfigs и после правок кита — хук потребителя видит лимит тела.
func TestModule_HumaConfigs(t *testing.T) {
	t.Setenv("HTTP_MAX_BODY_BYTES", "4096")
	var seenMaxBody int64
	edit := func(mark string) func(*huma.Config) {
		return func(c *huma.Config) { c.Info.Description += mark }
	}
	var api huma.API
	startApp(t,
		fx.Supply(httpkit.HumaConfigs{
			edit("a"), edit("b"),
			func(c *huma.Config) {
				c.OnAddOperation = append(c.OnAddOperation, func(_ *huma.OpenAPI, op *huma.Operation) {
					if op.OperationID == "get-health" {
						seenMaxBody = op.MaxBodyBytes
					}
				})
			},
			edit("c"),
		}),
		fx.Populate(&api),
	)
	if got := api.OpenAPI().Info.Description; got != "abc" {
		t.Fatalf("huma config edits applied as %q, want abc (slice order)", got)
	}
	if seenMaxBody != 4096 {
		t.Fatalf("a consumer OnAddOperation runs after the kit's: MaxBodyBytes = %d, want 4096", seenMaxBody)
	}
}
