package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/hubkit/client"
)

// recorder — сервер, запоминающий метод и полный URI последнего запроса.
type recorder struct {
	method atomic.Value
	uri    atomic.Value
	body   atomic.Value
}

// newRecorder отдаёт `{"data":null}` — годится и листингу, и одиночному ресурсу.
func newRecorder(t *testing.T) (*client.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method.Store(r.Method)
		rec.uri.Store(r.URL.RequestURI())
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.body.Store(body)
		_, _ = w.Write([]byte(`{"data":null}`))
	}))
	t.Cleanup(srv.Close)
	c := mustClient(t, client.Config{BaseURL: srv.URL, EmployeeBaseURL: srv.URL, TagBaseURL: srv.URL}, srv.Client())
	return c, rec
}

func (r *recorder) requestURI() string {
	uri, _ := r.uri.Load().(string)
	return uri
}

func (r *recorder) httpMethod() string {
	m, _ := r.method.Load().(string)
	return m
}

func TestListings_PathsAndQuery(t *testing.T) {
	tests := map[string]struct {
		call    func(*client.Client) error
		method  string
		wantURI string
	}{
		"companies": {
			call:   func(c *client.Client) error { _, err := c.ListCompanies(t.Context(), client.ListParams{}); return err },
			method: http.MethodGet, wantURI: "/api/companies",
		},
		"companies paged": {
			call: func(c *client.Client) error {
				_, err := c.ListCompanies(t.Context(), client.ListParams{Limit: 10, Offset: 20, IncludeArchived: true})
				return err
			},
			method: http.MethodGet, wantURI: "/api/companies?limit=10&offset=20&include_archived=true",
		},
		"company by slug": {
			call:   func(c *client.Client) error { _, err := c.GetCompanyBySlug(t.Context(), "a/b c"); return err },
			method: http.MethodGet, wantURI: "/api/companies/by-slug?slug=a%2Fb+c",
		},
		"company projects": {
			call: func(c *client.Client) error {
				_, err := c.ListCompanyProjects(t.Context(), "c 1", client.ListParams{Limit: 5})
				return err
			},
			method: http.MethodGet, wantURI: "/api/companies/c%201/projects?limit=5",
		},
		"unarchive company": {
			call:   func(c *client.Client) error { return c.UnarchiveCompany(t.Context(), "c1") },
			method: http.MethodPost, wantURI: "/api/companies/c1/unarchive",
		},
		"archive company": {
			call:   func(c *client.Client) error { return c.ArchiveCompany(t.Context(), "c1") },
			method: http.MethodPost, wantURI: "/api/companies/c1/archive",
		},
		"projects": {
			call: func(c *client.Client) error {
				_, err := c.ListProjects(t.Context(), client.ListParams{Offset: 3})
				return err
			},
			method: http.MethodGet, wantURI: "/api/projects?offset=3",
		},
		"project": {
			call:   func(c *client.Client) error { _, err := c.GetProject(t.Context(), "p1"); return err },
			method: http.MethodGet, wantURI: "/api/projects/p1",
		},
		"create project": {
			call: func(c *client.Client) error {
				_, err := c.CreateProject(t.Context(), client.ProjectInput{Name: "P"})
				return err
			},
			method: http.MethodPost, wantURI: "/api/projects",
		},
		"update project": {
			call: func(c *client.Client) error {
				_, err := c.UpdateProject(t.Context(), "p1", client.ProjectInput{Name: "P"})
				return err
			},
			method: http.MethodPatch, wantURI: "/api/projects/p1",
		},
		"archive project": {
			call:   func(c *client.Client) error { return c.ArchiveProject(t.Context(), "p1") },
			method: http.MethodPost, wantURI: "/api/projects/p1/archive",
		},
		"unarchive project": {
			call:   func(c *client.Client) error { return c.UnarchiveProject(t.Context(), "p1") },
			method: http.MethodPost, wantURI: "/api/projects/p1/unarchive",
		},
		"create company": {
			call: func(c *client.Client) error {
				_, err := c.CreateCompany(t.Context(), client.CompanyInput{Name: "Acme"})
				return err
			},
			method: http.MethodPost, wantURI: "/api/companies",
		},
		"visibility": {
			call:   func(c *client.Client) error { _, err := c.ListVisibility(t.Context(), "deal & co"); return err },
			method: http.MethodGet, wantURI: "/api/visibility?service=deal+%26+co",
		},
		"employees": {
			call: func(c *client.Client) error {
				_, err := c.ListEmployees(t.Context(), client.ListParams{Limit: 2})
				return err
			},
			method: http.MethodGet, wantURI: "/api/employees?limit=2",
		},
		"employee": {
			call:   func(c *client.Client) error { _, err := c.GetEmployee(t.Context(), "e 1"); return err },
			method: http.MethodGet, wantURI: "/api/employees/e%201",
		},
		"sync feed escapes resource": {
			call: func(c *client.Client) error {
				_, err := c.SyncFeed(t.Context(), "entity tags/../x", 3, 0)
				return err
			},
			method: http.MethodGet, wantURI: "/api/sync/entity%20tags%2F..%2Fx?after=3",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c, rec := newRecorder(t)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got := rec.requestURI(); got != tc.wantURI {
				t.Fatalf("uri = %q, want %q", got, tc.wantURI)
			}
			if got := rec.httpMethod(); got != tc.method {
				t.Fatalf("method = %q, want %q", got, tc.method)
			}
		})
	}
}

func TestAPIError_UnmappedStatus(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"detail":"nope"}`))
	})
	_, err := c.GetCompany(t.Context(), "c1")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *client.APIError", err)
	}
	if apiErr.Status != http.StatusTeapot || !strings.Contains(apiErr.Error(), "nope") {
		t.Fatalf("api error = %+v", apiErr)
	}
}

func TestTransportError_KeepsErrorChain(t *testing.T) {
	// Дедлайн вызывающего должен оставаться различимым под ErrUnavailable.
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})
	c := mustClient(t, client.Config{BaseURL: "http://hub"}, doer)
	err := c.ArchiveCompany(t.Context(), "c1")
	if !errors.Is(err, client.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want ErrUnavailable + DeadlineExceeded", err)
	}
}

func TestBodyDrainedForKeepAlive(t *testing.T) {
	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(strings.Repeat("x", 512)))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	c := mustClient(t, client.Config{BaseURL: srv.URL}, srv.Client())
	for range 3 {
		if err := c.ArchiveCompany(t.Context(), "c1"); !errors.Is(err, client.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	}
	if conns.Load() != 1 {
		t.Fatalf("connections = %d, want 1 (тело ответа не дренируется)", conns.Load())
	}
}

func TestIdempotentWritesAllowRetry(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": "t1", "name": "a"}})
	}))
	t.Cleanup(srv.Close)

	doer, err := httpclient.New(httpclient.Config{
		Timeout: 2 * time.Second, MaxRetries: 2, RetryWait: time.Millisecond, RetryMaxWait: 10 * time.Millisecond,
		MaxRedirects: 10, SSEMaxEventBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	c := mustClient(t, client.Config{BaseURL: srv.URL, TagBaseURL: srv.URL, EmployeeBaseURL: srv.URL}, doer)

	// POST-upsert дедуплицируется хабом по norm_name — помечен как retry-safe.
	if _, err := c.UpsertTag(t.Context(), "a"); err != nil {
		t.Fatalf("UpsertTag: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2 (ретрая не было)", attempts.Load())
	}

	// CreateCompany не идемпотентен: повтор дал бы вторую компанию.
	attempts.Store(0)
	if _, err := c.CreateCompany(t.Context(), client.CompanyInput{Name: "Acme"}); !errors.Is(err, client.ErrUnavailable) {
		t.Fatalf("CreateCompany err = %v, want ErrUnavailable", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("create attempts = %d, want 1", attempts.Load())
	}
}

func TestSyncFeed_TagCarriesFeedFields(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id": "t1", "name": "Ключевой", "norm_name": "ключевой",
				"created_at": "2026-01-02T03:04:05Z", "updated_seq": 42,
			}},
			"next_after": 42,
		})
	})
	page, err := c.SyncFeed(t.Context(), client.ResourceTags, 0, 100)
	if err != nil {
		t.Fatalf("SyncFeed: %v", err)
	}
	var tag client.Tag
	if err := json.Unmarshal(page.Data[0], &tag); err != nil {
		t.Fatalf("unmarshal tag: %v", err)
	}
	if tag.UpdatedSeq != 42 || tag.CreatedAt.IsZero() || tag.NormName != "ключевой" {
		t.Fatalf("tag = %+v", tag)
	}
}

func TestConfig_Validate(t *testing.T) {
	valid := client.Config{BaseURL: "https://api.hub.test", ServiceToken: "tok"}
	tests := map[string]struct {
		mutate func(*client.Config)
		ok     bool
	}{
		"valid":             {mutate: func(*client.Config) {}, ok: true},
		"per-concern hosts": {mutate: func(c *client.Config) { c.EmployeeBaseURL = "https://id.test"; c.TagBaseURL = "https://tags.test" }, ok: true},
		"no base url":       {mutate: func(c *client.Config) { c.BaseURL = "" }},
		"relative base url": {mutate: func(c *client.Config) { c.BaseURL = "/api" }},
		"no service token":  {mutate: func(c *client.Config) { c.ServiceToken = "" }},
		"bad employee host": {mutate: func(c *client.Config) { c.EmployeeBaseURL = "id.test" }},
		"bad tag host":      {mutate: func(c *client.Config) { c.TagBaseURL = "://" }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			err := cfg.Validate()
			switch {
			case tc.ok && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case !tc.ok && !errors.Is(err, client.ErrConfig):
				t.Fatalf("Validate() = %v, want ErrConfig", err)
			}
		})
	}
}

func TestNew_RejectsInvalidConfig(t *testing.T) {
	if _, err := client.New(client.Config{}, nil); !errors.Is(err, client.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewConfig_FromEnv(t *testing.T) {
	t.Setenv("HUB_BASE_URL", "https://api.hub.test")
	if _, err := client.NewConfig(); !errors.Is(err, client.ErrConfig) {
		t.Fatalf("without service token: %v", err)
	}
	t.Setenv("HUB_SERVICE_TOKEN", "tok")
	t.Setenv("HUB_TAG_BASE_URL", "https://tags.hub.test")
	cfg, err := client.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.TagBaseURL != "https://tags.hub.test" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestModule_Graph(t *testing.T) {
	t.Setenv("HUB_BASE_URL", "https://api.hub.test")
	t.Setenv("HUB_SERVICE_TOKEN", "tok")
	var c *client.Client
	app := fxtest.New(
		t,
		fx.Provide(client.NewConfig),
		client.Module,
		fx.Populate(&c),
	)
	app.RequireStart().RequireStop()
	if c == nil {
		t.Fatal("Module did not provide *client.Client")
	}
}
