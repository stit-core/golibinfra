package sync_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/client"
	hubsync "git.stit.tech/stit-core/golibinfra/hubkit/sync"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/hubtest"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

func testConfig() hubsync.Config {
	return hubsync.Config{Interval: time.Minute, Limit: 2, Timeout: time.Minute, Jitter: time.Second}
}

func newEngine(t *testing.T, resource string, feed *hubtest.Feed, proj *hubtest.Projection, cur *hubtest.Cursors, opts ...hubsync.Option) *hubsync.Engine {
	t.Helper()
	e, err := hubsync.NewEngine(resource, feed, proj, cur, testConfig(), logkit.Nop(), opts...)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return closeAfterTest(t, e)
}

// closeAfterTest снимает observable-callback движка по концу теста: иначе
// серии предыдущего прогона видны следующему (`-count>1`).
func closeAfterTest(t *testing.T, e *hubsync.Engine) *hubsync.Engine {
	t.Helper()
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return e
}

func company(id string, seq int64) map[string]any {
	return map[string]any{"id": id, "name": id, "updated_seq": seq}
}

func TestRun_CatchUpMultiPage(t *testing.T) {
	feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
	for i := range int64(5) {
		feed.Add(t, client.ResourceCompanies, i+1, company("c", i+1))
	}
	e := newEngine(t, client.ResourceCompanies, feed, proj, cur)

	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(proj.Items(client.ResourceCompanies)); got != 5 {
		t.Fatalf("upserted = %d, want 5", got)
	}
	if got := cur.Cursor(client.ResourceCompanies); got != 5 {
		t.Fatalf("cursor = %d, want 5", got)
	}
}

func TestRun_IdempotentAndIncremental(t *testing.T) {
	feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
	feed.Add(t, client.ResourceCompanies, 1, company("c1", 1))
	e := newEngine(t, client.ResourceCompanies, feed, proj, cur)

	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := len(proj.Items(client.ResourceCompanies)); got != 1 {
		t.Fatalf("догнанный фид переприменил записи: %d", got)
	}

	feed.Add(t, client.ResourceCompanies, 2, company("c2", 2))
	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("incremental run: %v", err)
	}
	if got := cur.Cursor(client.ResourceCompanies); got != 2 {
		t.Fatalf("cursor = %d, want 2", got)
	}

	// Сброс курсора продукта = переигрывание фида (апсерт идемпотентен).
	cur.Set(client.ResourceCompanies, 0)
	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("redelivery run: %v", err)
	}
	if got := len(proj.Items(client.ResourceCompanies)); got != 4 {
		t.Fatalf("redelivery upserts = %d, want 4", got)
	}
}

func TestRun_NextCursorFallsBackToUpdatedSeq(t *testing.T) {
	feed := &noNextAfterFeed{items: []json.RawMessage{
		mustJSON(t, company("c1", 7)),
		mustJSON(t, company("c2", 9)),
	}}
	proj, cur := hubtest.NewProjection(), hubtest.NewCursors()
	e, err := hubsync.NewEngine(client.ResourceCompanies, feed, proj, cur, testConfig(), logkit.Nop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	closeAfterTest(t, e)
	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := cur.Cursor(client.ResourceCompanies); got != 9 {
		t.Fatalf("cursor = %d, want max(updated_seq) = 9", got)
	}
}

func TestRun_StopsWhenCursorDoesNotAdvance(t *testing.T) {
	feed := &stuckFeed{item: mustJSON(t, map[string]any{"id": "c1"})}
	proj, cur := hubtest.NewProjection(), hubtest.NewCursors()
	e, err := hubsync.NewEngine(client.ResourceCompanies, feed, proj, cur, testConfig(), logkit.Nop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	closeAfterTest(t, e)
	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if feed.calls != 1 {
		t.Fatalf("fetches = %d, want 1 (зацикливание)", feed.calls)
	}
}

func TestRun_ErrorBranches(t *testing.T) {
	boom := errors.New("boom")
	tests := map[string]func(*hubtest.Feed, *hubtest.Projection, *hubtest.Cursors){
		"load cursor": func(_ *hubtest.Feed, _ *hubtest.Projection, c *hubtest.Cursors) { c.FailLoad(boom) },
		"fetch":       func(f *hubtest.Feed, _ *hubtest.Projection, _ *hubtest.Cursors) { f.FailWith("companies", boom) },
		"upsert":      func(_ *hubtest.Feed, p *hubtest.Projection, _ *hubtest.Cursors) { p.FailWith(boom) },
		"save cursor": func(_ *hubtest.Feed, _ *hubtest.Projection, c *hubtest.Cursors) { c.FailSave(boom) },
	}
	for name, fail := range tests {
		t.Run(name, func(t *testing.T) {
			resource := "companies-" + name
			feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
			feed.Add(t, resource, 1, company("c1", 1))
			feed.FailWith("companies", nil)
			fail(feed, proj, cur)
			if name == "fetch" {
				feed.FailWith(resource, boom)
			}
			e := newEngine(t, resource, feed, proj, cur)

			before := counterValue(t, "hub_sync_errors_total", resource)
			if err := e.Run(t.Context()); !errors.Is(err, boom) {
				t.Fatalf("Run err = %v, want boom", err)
			}
			if got := counterValue(t, "hub_sync_errors_total", resource); got <= before {
				t.Fatalf("hub_sync_errors_total = %d, want > %d", got, before)
			}
		})
	}
}

func TestRun_CancellationIsNotAnError(t *testing.T) {
	resource := "companies-cancel"
	feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
	feed.FailWith(resource, context.Canceled)
	e := newEngine(t, resource, feed, proj, cur)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := e.Run(ctx); err != nil {
		t.Fatalf("Run on cancelled ctx = %v, want nil", err)
	}
	if got := counterValue(t, "hub_sync_errors_total", resource); got != -1 {
		t.Fatalf("hub_sync_errors_total = %d, want no series", got)
	}
}

func TestRun_CursorGaugeAfterFirstLoad(t *testing.T) {
	resource := "companies-gauge"
	feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
	cur.Set(resource, 42)
	e := newEngine(t, resource, feed, proj, cur)

	// До первого тика курсор неизвестен — серии нет (0 врал бы про «фид с начала»).
	if got := gaugeValue(t, "hub_sync_cursor_seq", resource); got != -1 {
		t.Fatalf("gauge before first tick = %d, want no series", got)
	}
	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := gaugeValue(t, "hub_sync_cursor_seq", resource); got != 42 {
		t.Fatalf("gauge after no-op tick = %d, want 42", got)
	}
}

func TestNewEngine_Validation(t *testing.T) {
	feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
	bad := map[string]hubsync.Config{
		"zero interval":    {Interval: 0, Limit: 10},
		"zero limit":       {Interval: time.Minute, Limit: 0},
		"limit too large":  {Interval: time.Minute, Limit: 1_000_000},
		"negative timeout": {Interval: time.Minute, Limit: 10, Timeout: -time.Second},
		"negative jitter":  {Interval: time.Minute, Limit: 10, Jitter: -time.Second},
	}
	for name, cfg := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := hubsync.NewEngine("companies", feed, proj, cur, cfg, logkit.Nop()); !errors.Is(err, hubsync.ErrConfig) {
				t.Fatalf("err = %v, want ErrConfig", err)
			}
		})
	}
	if _, err := hubsync.NewEngine("", feed, proj, cur, testConfig(), logkit.Nop()); !errors.Is(err, hubsync.ErrConfig) {
		t.Fatalf("empty resource: %v", err)
	}
	if _, err := hubsync.NewEngine("companies", nil, proj, cur, testConfig(), logkit.Nop()); !errors.Is(err, hubsync.ErrConfig) {
		t.Fatalf("nil fetcher: %v", err)
	}
}

func TestEngine_Job(t *testing.T) {
	feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
	cfg := testConfig()
	cfg.RunOnStart = true
	e, err := hubsync.NewEngine("companies", feed, proj, cur, cfg, logkit.Nop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	closeAfterTest(t, e)
	if e.Resource() != "companies" {
		t.Fatalf("Resource() = %q", e.Resource())
	}
	job := e.Job()
	if job.Name != "hubsync.companies" || job.Interval != cfg.Interval || job.Timeout != cfg.Timeout {
		t.Fatalf("job = %+v", job)
	}
	if job.Jitter != cfg.Jitter || !job.RunOnStart {
		t.Fatalf("job schedule options lost: %+v", job)
	}
}

func TestEngine_ClockInjected(t *testing.T) {
	clock := clockkit.NewFixed(time.Unix(1_700_000_000, 0))
	feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
	e := newEngine(t, "companies-clock", feed, proj, cur, hubsync.WithClock(clock))
	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestHTTPFetcher(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/companies" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("after") != "4" || r.URL.Query().Get("limit") != "2" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       []map[string]any{company("c5", 5)},
			"next_after": 5,
		})
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{BaseURL: srv.URL, ServiceToken: "tok"}, srv.Client())
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	f := hubsync.NewHTTPFetcher(c)

	page, err := f.Fetch(t.Context(), client.ResourceCompanies, 4, 2)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if page.NextAfter != 5 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if _, err := f.Fetch(t.Context(), "unknown", 0, 2); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("Fetch unknown resource: %v", err)
	}
}

func TestEngine_CloseUnregistersObservables(t *testing.T) {
	resource := "companies-close"
	feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
	cur.Set(resource, 7)
	e, err := hubsync.NewEngine(resource, feed, proj, cur, testConfig(), logkit.Nop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := e.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := gaugeValue(t, "hub_sync_cursor_seq", resource); got != 7 {
		t.Fatalf("gauge = %d, want 7", got)
	}

	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := gaugeValue(t, "hub_sync_cursor_seq", resource); got != -1 {
		t.Fatalf("gauge after Close = %d, want no series", got)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestModule_Graph(t *testing.T) {
	// Движок этого теста доходит до сети: пустая страница вместо DNS-запроса.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HUB_BASE_URL", srv.URL)
	t.Setenv("HUB_SERVICE_TOKEN", "tok")
	t.Setenv("HUB_SYNC_INTERVAL", "1h")
	// Движок тест гонит сам: job timerkit на старте не должен идти параллельно.
	t.Setenv("HUB_SYNC_RUN_ON_START", "false")

	resource := "companies-module"
	cur := hubtest.NewCursors()
	cur.Set(resource, 11)

	var jobs []timerkit.Job
	var engine *hubsync.Engine
	type jobsIn struct {
		fx.In
		Jobs []timerkit.Job `group:"timerkit.jobs"`
	}
	app := fxtest.New(
		t,
		fx.Provide(client.NewConfig, hubsync.NewConfig),
		fx.Provide(logkit.Nop),
		client.Module,
		timerkit.Module,
		hubsync.Module,
		fx.Provide(fx.Annotate(
			func(f hubsync.Fetcher, cfg hubsync.Config, log logkit.Logger) (*hubsync.Engine, error) {
				e, err := hubsync.NewEngine(resource, f, hubtest.NewProjection(), cur, cfg, log)
				engine = e
				return e, err
			},
			fx.ResultTags(`group:"hubkit.sync.engine"`),
		)),
		fx.Invoke(func(in jobsIn) { jobs = in.Jobs }),
	)
	app.RequireStart()
	if err := engine.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := gaugeValue(t, "hub_sync_cursor_seq", resource); got != 11 {
		t.Fatalf("gauge = %d, want 11", got)
	}
	app.RequireStop()

	if len(jobs) != 1 || jobs[0].Name != "hubsync."+resource {
		t.Fatalf("timer jobs = %+v", jobs)
	}
	// Без OnStop движок остановленного приложения продолжал бы публиковать серии.
	if got := gaugeValue(t, "hub_sync_cursor_seq", resource); got != -1 {
		t.Fatalf("gauge after app stop = %d, want no series", got)
	}
}

func TestNewConfig_FromEnv(t *testing.T) {
	cfg, err := hubsync.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Interval != 30*time.Second || cfg.Limit != 500 || !cfg.RunOnStart {
		t.Fatalf("defaults: %+v", cfg)
	}
	t.Setenv("HUB_SYNC_LIMIT", "0")
	if _, err := hubsync.NewConfig(); !errors.Is(err, hubsync.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// noNextAfterFeed — фид без next_after: курсор считается по updated_seq батча.
type noNextAfterFeed struct {
	items []json.RawMessage
	done  bool
}

func (f *noNextAfterFeed) Fetch(context.Context, string, int64, int) (hubsync.Page, error) {
	if f.done {
		return hubsync.Page{}, nil
	}
	f.done = true
	return hubsync.Page{Items: f.items}, nil
}

// stuckFeed отдаёт элементы без updated_seq: курсор не двигается.
type stuckFeed struct {
	item  json.RawMessage
	calls int
}

func (f *stuckFeed) Fetch(context.Context, string, int64, int) (hubsync.Page, error) {
	f.calls++
	return hubsync.Page{Items: []json.RawMessage{f.item}}, nil
}
