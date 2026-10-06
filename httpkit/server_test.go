package httpkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
)

func validConfig() Config {
	return Config{
		Addr:              ":0",
		ShutdownTimeout:   time.Second,
		ReadyProbeTimeout: time.Second,
		APITitle:          "t",
		APIVersion:        "0",
		DocsEnabled:       true,
		MaxBody:           MaxBodyConfig{MaxBytes: 1 << 20},
		Idempotency: IdempotencyConfig{
			Header: "Idempotency-Key", TTL: time.Hour, ReserveTTL: time.Minute,
			MaxBodyBytes: 1024, StoreTimeout: time.Second,
		},
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "zero value", mutate: func(c *Config) { *c = Config{} }, wantErr: true},
		{name: "empty addr", mutate: func(c *Config) { c.Addr = "" }, wantErr: true},
		{name: "negative read timeout", mutate: func(c *Config) { c.ReadTimeout = -time.Second }, wantErr: true},
		{name: "negative write timeout", mutate: func(c *Config) { c.WriteTimeout = -time.Second }, wantErr: true},
		{name: "zero shutdown timeout", mutate: func(c *Config) { c.ShutdownTimeout = 0 }, wantErr: true},
		{name: "negative drain delay", mutate: func(c *Config) { c.ShutdownDrainDelay = -time.Second }, wantErr: true},
		{name: "zero ready probe timeout", mutate: func(c *Config) { c.ReadyProbeTimeout = 0 }, wantErr: true},
		{name: "negative trust proxy hops", mutate: func(c *Config) { c.TrustProxyHops = -1 }, wantErr: true},
		{name: "empty api title", mutate: func(c *Config) { c.APITitle = "" }, wantErr: true},
		{name: "empty api version", mutate: func(c *Config) { c.APIVersion = "" }, wantErr: true},
		{name: "negative rps", mutate: func(c *Config) { c.RateLimit.RPS = -1 }, wantErr: true},
		{name: "negative burst", mutate: func(c *Config) { c.RateLimit.Burst = -1 }, wantErr: true},
		{name: "negative max body", mutate: func(c *Config) { c.MaxBody.MaxBytes = -1 }, wantErr: true},
		{name: "max body off", mutate: func(c *Config) { c.MaxBody.MaxBytes = 0 }},
		{name: "empty idempotency header", mutate: func(c *Config) { c.Idempotency.Header = "" }, wantErr: true},
		{name: "zero idempotency ttl", mutate: func(c *Config) { c.Idempotency.TTL = 0 }, wantErr: true},
		{name: "zero idempotency reserve ttl", mutate: func(c *Config) { c.Idempotency.ReserveTTL = 0 }, wantErr: true},
		{name: "zero idempotency body limit", mutate: func(c *Config) { c.Idempotency.MaxBodyBytes = 0 }, wantErr: true},
		{name: "zero idempotency store timeout", mutate: func(c *Config) { c.Idempotency.StoreTimeout = 0 }, wantErr: true},
		{name: "negative cors max age", mutate: func(c *Config) { c.CORS.MaxAge = -1 }, wantErr: true},
		{
			name:    "wildcard origin with credentials",
			mutate:  func(c *Config) { c.CORS = CORSConfig{Origins: []string{"*"}, AllowCredentials: true} },
			wantErr: true,
		},
		{
			name: "listed origin with credentials",
			mutate: func(c *Config) {
				c.CORS = CORSConfig{Origins: []string{"https://app.example.com"}, AllowCredentials: true}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr && !errors.Is(err, ErrConfig) {
				t.Fatalf("want ErrConfig, got %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("valid config rejected: %v", err)
			}
		})
	}
}

func TestBindAPI_NoSchemes(t *testing.T) {
	s := NewServer(validConfig())
	api := s.BindAPI()
	if api.OpenAPI().Components.SecuritySchemes != nil {
		t.Fatalf("expected nil SecuritySchemes when no entries provided, got %v", api.OpenAPI().Components.SecuritySchemes)
	}
}

func TestBindAPI_WithSchemes(t *testing.T) {
	cookie := SecuritySchemeEntry{
		Name:   "Cookie",
		Scheme: &huma.SecurityScheme{Type: "apiKey", In: "cookie", Name: "sid"},
	}
	bearer := SecuritySchemeEntry{
		Name:   "Bearer",
		Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
	}

	s := NewServer(validConfig(), WithSecuritySchemes(cookie, bearer))
	api := s.BindAPI()

	got := api.OpenAPI().Components.SecuritySchemes
	if len(got) != 2 {
		t.Fatalf("want 2 schemes, got %d (%v)", len(got), got)
	}
	if got["Cookie"] != cookie.Scheme || got["Bearer"] != bearer.Scheme {
		t.Errorf("scheme mismatch: %+v", got)
	}
}

func TestBindAPI_Idempotent(t *testing.T) {
	s := NewServer(validConfig())
	if s.BindAPI() != s.BindAPI() {
		t.Fatal("BindAPI must return same api on repeat call")
	}
}

func TestBindAPI_DocsDisabled(t *testing.T) {
	cfg := validConfig()
	cfg.DocsEnabled = false
	s := NewServer(cfg)
	s.BindAPI()

	for _, path := range []string{"/docs", "/openapi.json", "/openapi.yaml"} {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s with DocsEnabled=false: want 404, got %d", path, rec.Code)
		}
	}

	s = NewServer(validConfig())
	s.BindAPI()
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/openapi.json with DocsEnabled=true: want 200, got %d", rec.Code)
	}
}

// Регрессия: huma режет тело каждой операции своим дефолтом 1 MiB, поэтому
// HTTP_MAX_BODY_BYTES должен доезжать до Operation.MaxBodyBytes.
func TestBindAPI_MaxBodyBytesReachesOperations(t *testing.T) {
	cfg := validConfig()
	cfg.MaxBody.MaxBytes = 16
	s := NewServer(cfg)
	api := s.BindAPI()

	huma.Register(api, huma.Operation{
		OperationID: "echo", Method: http.MethodPost, Path: "/echo",
	}, func(_ context.Context, _ *struct {
		Body struct {
			Name string `json:"name"`
		}
	},
	) (*struct{}, error) {
		return nil, nil //nolint:nilnil // тело ответа не нужно
	})
	if op := api.OpenAPI().Paths["/echo"].Post; op.MaxBodyBytes != 16 {
		t.Fatalf("Operation.MaxBodyBytes = %d, want 16", op.MaxBodyBytes)
	}
}

func TestWithHumaConfig(t *testing.T) {
	s := NewServer(validConfig(), WithHumaConfig(func(c *huma.Config) {
		c.Info.Description = "patched"
	}))
	if got := s.BindAPI().OpenAPI().Info.Description; got != "patched" {
		t.Fatalf("huma config hook not applied: %q", got)
	}
}

func TestWithMiddleware(t *testing.T) {
	var seen []string
	mw := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = append(seen, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	s := NewServer(validConfig(), WithMiddleware(mw("outer"), mw("inner")))
	api := s.BindAPI()
	huma.Get(api, "/ping", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, nil //nolint:nilnil // тело ответа не нужно
	})

	s.Router().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	if len(seen) != 2 || seen[0] != "outer" || seen[1] != "inner" {
		t.Fatalf("middleware order: %v", seen)
	}
}

func TestServer_ListenAddrAndStop(t *testing.T) {
	cfg := validConfig()
	cfg.Addr = "127.0.0.1:0"
	s := NewServer(cfg)
	if s.Addr() != nil {
		t.Fatal("Addr before Listen must be nil")
	}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("Listen must be idempotent: %v", err)
	}
	if s.Addr() == nil {
		t.Fatal("Addr after Listen")
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve after Stop must return nil, got %v", err)
	}
}

// Регрессия: APIError из huma-хендлера должен уходить как problem+json —
// тем же content-type, что и ошибки из chi-middleware.
func TestAPIError_ContentTypeFromHumaHandler(t *testing.T) {
	_, api := humatest.New(t, huma.DefaultConfig("t", "0"))
	huma.Get(api, "/missing", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, NotFound("USER_NOT_FOUND", "user does not exist")
	})

	resp := api.Get("/missing")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); ct != ProblemContentType {
		t.Fatalf("content-type = %q, want %q", ct, ProblemContentType)
	}
}
