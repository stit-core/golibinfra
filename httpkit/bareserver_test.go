package httpkit_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// Регрессия (NF1): нейтрализация 5xx — свойство пакета, а не логгера: документированный путь
// «Без fx» не отдаёт клиенту текст ошибки с DSN.
func TestServer_NeutralizesUnmappedErrorWithoutLogger(t *testing.T) {
	// Проводка из docs/usage.md § Прямой запуск без fx с logkit.Nop() вместо логгера.
	s := mustServer(t, testConfig(t, "127.0.0.1:0"))
	huma.Get(s.BindAPI(), "/boom", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, errors.New("pq: dial tcp 10.0.0.5:5432 user=admin password=s3cret")
	})
	if err := s.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(stopCtx)
	})

	resp := get(t, s, "/boom")
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
}

// humatest в тестах потребителя — та же форма ошибок: ошибки huma подменяет импорт httpkit.
func TestHumatest_NeutralizesUnmappedError(t *testing.T) {
	_, api := humatest.New(t, huma.DefaultConfig("t", "0"))
	huma.Get(api, "/boom", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, errors.New("pq: password=s3cret")
	})
	resp := api.Get("/boom")
	if resp.Code != http.StatusInternalServerError || resp.Header().Get("Content-Type") != httpkit.ProblemContentType {
		t.Fatalf("want 500 problem+json, got %d %v", resp.Code, resp.Header())
	}
	if body := resp.Body.String(); strings.Contains(body, "s3cret") || !strings.Contains(body, `"code":"INTERNAL"`) {
		t.Fatalf("humatest must get the kit's neutral 500: %s", body)
	}
}

// Регрессия KAPI-08: литерал Config без таймаутов — ErrConfig, а не сервер без защиты от
// slowloris и лимита тела.
func TestNewServer_RejectsInvalidConfig(t *testing.T) {
	if _, err := httpkit.NewServer(httpkit.Config{Addr: "127.0.0.1:0"}, logkit.Nop()); !errors.Is(err, httpkit.ErrConfig) {
		t.Fatalf("NewServer(literal) err = %v, want ErrConfig", err)
	}
}

// Путь без fx — та же сборка, что у Module: цепочка кита (request-id, security-заголовки,
// Recovery, лимит тела) и HumaAuth с extractor'ом схемы из WithSecuritySchemes.
func TestNewServer_AssemblesKitChain(t *testing.T) {
	cfg := testConfig(t, "127.0.0.1:0")
	cfg.MaxBody.MaxBytes = 8
	extract := func(r *http.Request) (context.Context, error) {
		if r.Header.Get("Authorization") != "Bearer ok" {
			return nil, errors.New("no token")
		}
		return httpkit.ContextWithSubject(r.Context(), "u1"), nil
	}
	s := mustServer(t, cfg,
		httpkit.WithSecuritySchemes(httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: bearerScheme, Extract: extract}))
	api := s.BindAPI()
	huma.Register(api, httpkit.Public(operation("boom", http.MethodGet, "/boom")), func(context.Context, *struct{}) (*struct{}, error) {
		panic("boom")
	})
	huma.Register(api, httpkit.Public(operation("echo", http.MethodPost, "/echo")), func(context.Context, *struct{ Body map[string]any }) (*struct{}, error) {
		return nil, nil //nolint:nilnil // тело ответа не нужно
	})
	secured := operation("me", http.MethodGet, "/me")
	secured.Security = []map[string][]string{{"Bearer": {}}}
	huma.Register(api, secured, noBody)

	serve := func(method, path, body string, header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, req)
		return rec
	}
	boom := serve(http.MethodGet, "/boom", "")
	if boom.Code != http.StatusInternalServerError || boom.Header().Get("X-Request-ID") == "" || boom.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("panic: %d %v, want 500 with X-Request-ID and security headers", boom.Code, boom.Header())
	}
	if big := serve(http.MethodPost, "/echo", `{"x":"0123456789"}`); big.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body over HTTP_MAX_BODY_BYTES: %d, want 413", big.Code)
	}
	if anon := serve(http.MethodGet, "/me", ""); anon.Code != http.StatusUnauthorized {
		t.Fatalf("secured without token: %d, want 401", anon.Code)
	}
	if ok := serve(http.MethodGet, "/me", "", "Authorization", "Bearer ok"); ok.Code != http.StatusNoContent {
		t.Fatalf("secured with token: %d, want 204", ok.Code)
	}
}

// readmeWithoutFx — сниппет README § Быстрый старт → «Без fx»: тело до `return nil` равно блоку
// README байт в байт (TestReadme_WithoutFx).
func readmeWithoutFx(cfg httpkit.Config, log logkit.Logger, bearer httpkit.SecuritySchemeEntry, db *pgkit.DB, handler func(context.Context, *struct{}) (*struct{}, error)) error {
	s, err := httpkit.NewServer(cfg, log, httpkit.WithSecuritySchemes(bearer)) // bearer — SecuritySchemeEntry с Extract
	if err != nil {
		return err
	}
	api := s.BindAPI()
	httpkit.RegisterHealth(api, cfg.ReadyProbeTimeout, log, pgkit.Probe(db))
	huma.Register(api, httpkit.Public(huma.Operation{Method: http.MethodGet, Path: "/ping"}), handler) // при схеме — Security или Public
	if err := s.CheckRoutes(); err != nil {
		return err
	}
	_ = s.Serve()
	return nil
}

// Блок README § Быстрый старт → «Без fx» — тело readmeWithoutFx байт в байт (отступ README —
// четыре пробела): сниппет правится только вместе с кодом и проходит проверку старта как написан —
// при схеме в WithSecuritySchemes операция без Security и без Public — отказ CheckRoutes.
func TestReadme_WithoutFx(t *testing.T) {
	_, rest, _ := strings.Cut(readFile(t, "bareserver_test.go"), "\nfunc readmeWithoutFx(")
	_, rest, _ = strings.Cut(rest, " error {\n")
	body, _, ok := strings.Cut(rest, "\treturn nil\n}\n")
	snippet := strings.ReplaceAll(strings.ReplaceAll("\n"+body, "\n\t", "\n"), "\t", "    ")[1:]
	if !ok || !strings.Contains(readFile(t, "README.md"), "```go\n"+snippet+"```\n") {
		t.Fatalf("README.md quick start: the block under «Без fx» is not the body of readmeWithoutFx; edit both:\n%s", snippet)
	}

	// Порт занят: Serve сниппета возвращается сразу, а не обслуживает до Stop.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	bearer := httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: bearerScheme, Extract: func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}}
	if err := readmeWithoutFx(testConfig(t, busy.Addr().String()), logkit.Nop(), bearer, nil, noBody); err != nil {
		t.Fatalf("snippet as written: %v", err)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Регрессия KCOR-12: BindAPI из нескольких горутин собирает один huma.API (под -race — без гонки).
func TestServer_BindAPIConcurrent(t *testing.T) {
	s := mustServer(t, testConfig(t, "127.0.0.1:0"))
	apis := make([]huma.API, 8)
	var wg sync.WaitGroup
	for i := range apis {
		wg.Go(func() { apis[i] = s.API() })
	}
	wg.Wait()
	for _, api := range apis {
		if api != apis[0] {
			t.Fatal("BindAPI built several huma.API on one router")
		}
	}
}

// nil-опции игнорируются: middleware и правка huma.Config без функции не роняют сборку.
func TestNewServer_NilOptionsIgnored(t *testing.T) {
	s, err := httpkit.NewServer(testConfig(t, "127.0.0.1:0"), nil, nil,
		httpkit.WithMiddleware(nil), httpkit.WithHumaConfig(nil),
		httpkit.WithIdempotencyStore(nil))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	huma.Register(s.BindAPI(), httpkit.Public(operation("ping", http.MethodGet, "/ping")), noBody)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("GET /ping = %d, want 204", rec.Code)
	}
}
