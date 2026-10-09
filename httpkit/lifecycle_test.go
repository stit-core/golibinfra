package httpkit_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

// hookOrder — кто добавил каждый выполненный хук (CallerName), в порядке выполнения.
type hookOrder struct {
	mu          sync.Mutex
	start, stop []string
}

func (h *hookOrder) LogEvent(e fxevent.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch ev := e.(type) {
	case *fxevent.OnStartExecuting:
		h.start = append(h.start, ev.CallerName)
	case *fxevent.OnStopExecuting:
		h.stop = append(h.stop, ev.CallerName)
	}
}

func (h *hookOrder) snapshot() (start, stop []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.start), slices.Clone(h.stop)
}

// resource — зависимость хендлера со своим хуком: открывается в OnStart, закрывается в OnStop.
type resource struct{ open atomic.Bool }

func newResource(lc fx.Lifecycle) *resource {
	r := &resource{}
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error { r.open.Store(true); return nil },
		OnStop:  func(context.Context) error { r.open.Store(false); return nil },
	})
	return r
}

// slowGate держит запрос в хендлере, пока тест не отпустит его.
type slowGate struct {
	entered, release chan struct{}
	once             sync.Once
}

func (g *slowGate) open() { g.once.Do(func() { close(g.release) }) }

// registerSlow — маршрут, который в полёте ждёт тест и отвечает 200, только пока ресурс открыт.
func registerSlow(api huma.API, res *resource, gate *slowGate) {
	huma.Get(api, "/slow", func(context.Context, *struct{}) (*struct{}, error) {
		gate.entered <- struct{}{}
		<-gate.release
		if !res.open.Load() {
			return nil, httpkit.Unavailable("RESOURCE_CLOSED", "resource closed before the request finished")
		}
		return nil, nil // тело ответа не нужно
	})
}

func isServerHook(caller string) bool { return strings.Contains(caller, "/golibinfra/httpkit.") }

// TestModule_Lifecycle: HTTP слушает после OnStart всех fx-модулей и гасится до их OnStop, где бы
// httpkit.Module ни стоял среди опций fx.New; запрос в полёте на остановке видит живой ресурс.
func TestModule_Lifecycle(t *testing.T) {
	resources := fx.Module("resources", fx.Provide(newResource), fx.Invoke(func(*resource) {}))
	routes := fx.Module("routes", fx.Invoke(registerSlow))
	orders := map[string][]fx.Option{
		"httpkit первым":     {httpkit.Module, resources, routes},
		"httpkit в середине": {routes, httpkit.Module, resources},
		"httpkit последним":  {resources, routes, httpkit.Module},
	}
	for name, modules := range orders {
		t.Run(name, func(t *testing.T) {
			hooks := &hookOrder{}
			gate := &slowGate{entered: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(gate.open)
			var (
				srv *httpkit.Server
				res *resource
			)
			app := fx.New(append([]fx.Option{
				fx.WithLogger(func() fxevent.Logger { return hooks }),
				fx.Supply(testConfig(t, "127.0.0.1:0"), gate),
				fx.Provide(logkit.Nop),
				fx.Populate(&srv, &res),
			}, modules...)...)
			startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := app.Start(startCtx); err != nil {
				t.Fatalf("start: %v", err)
			}

			status := make(chan int, 1)
			go func() {
				req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+srv.Addr().String()+"/slow", nil)
				if err != nil {
					status <- 0
					return
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					status <- 0
					return
				}
				_ = resp.Body.Close()
				status <- resp.StatusCode
			}()
			<-gate.entered

			stopped := make(chan error, 1)
			go func() {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer stopCancel()
				stopped <- app.Stop(stopCtx)
			}()
			waitListenerClosed(t, srv.Addr().String())
			if !res.open.Load() {
				t.Fatal("ресурс закрыт раньше, чем HTTP перестал принимать запросы")
			}
			gate.open()

			if got := <-status; got != http.StatusNoContent {
				t.Fatalf("запрос в полёте на остановке: статус %d, ждали 204 при живом ресурсе", got)
			}
			if err := <-stopped; err != nil {
				t.Fatalf("stop: %v", err)
			}
			if res.open.Load() {
				t.Fatal("ресурс не закрыт после остановки")
			}

			start, stop := hooks.snapshot()
			if len(start) < 2 || !isServerHook(start[len(start)-1]) || slices.ContainsFunc(start[:len(start)-1], isServerHook) {
				t.Fatalf("хук сервера должен стартовать последним: %v", start)
			}
			if len(stop) < 2 || !isServerHook(stop[0]) || slices.ContainsFunc(stop[1:], isServerHook) {
				t.Fatalf("хук сервера должен останавливаться первым: %v", stop)
			}
		})
	}
}

// waitListenerClosed ждёт, пока сервер перестанет принимать соединения: Shutdown закрыл listener.
func waitListenerClosed(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(context.Background(), "tcp", addr)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("сервер %s не закрыл listener на остановке", addr)
}

// readyStatus — /ready новым соединением: listener ещё принимает их, пока идёт пауза дренажа.
func readyStatus(addr string) (status int, body string, err error) {
	c := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/ready", nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

// Пауза дренажа: с начала остановки /ready отвечает 503 draining, а listener ещё принимает
// соединения — балансировщик выводит инстанс раньше, чем клиенты получат отказ соединения.
func TestModule_StopDrainsReadyBeforeClosingListener(t *testing.T) {
	t.Setenv("HTTP_SHUTDOWN_DRAIN_DELAY", "400ms")
	var srv *httpkit.Server
	app := fx.New(fx.NopLogger, fx.Supply(testConfig(t, "127.0.0.1:0")), fx.Provide(logkit.Nop),
		httpkit.Module, fx.Populate(&srv))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	addr := srv.Addr().String()
	if status, body, err := readyStatus(addr); err != nil || status != http.StatusOK {
		t.Fatalf("/ready before stop: %d %s %v", status, body, err)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- app.Stop(ctx) }()
	var status int
	var body string
	deadline := time.Now().Add(time.Second)
	for status != http.StatusServiceUnavailable && time.Now().Before(deadline) {
		var err error
		if status, body, err = readyStatus(addr); err != nil {
			t.Fatalf("/ready during the drain pause must answer, got %v", err)
		}
	}
	if status != http.StatusServiceUnavailable || !strings.Contains(body, `"draining"`) {
		t.Fatalf("/ready during the drain pause: %d %s, want 503 draining", status, body)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, _, err := readyStatus(addr); err == nil {
		t.Fatal("listener still accepts connections after Stop")
	}
}

// Пауза дренажа не выходит из бюджета остановки: длиннее двух третей бюджета fx она
// обрывается на них, и хук после HTTP получает остаток.
func TestModule_StopDrainPauseKeepsBudget(t *testing.T) {
	t.Setenv("HTTP_SHUTDOWN_DRAIN_DELAY", "5s")
	var left atomic.Int64
	later := fx.Module("later", fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{OnStop: func(ctx context.Context) error {
			if deadline, ok := ctx.Deadline(); ok {
				left.Store(int64(time.Until(deadline)))
			}
			return nil
		}})
	}))
	app := fx.New(fx.NopLogger, fx.Supply(testConfig(t, "127.0.0.1:0")), fx.Provide(logkit.Nop), later, httpkit.Module)
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start: %v", err)
	}

	const budget = 1500 * time.Millisecond
	stopCtx, stopCancel := context.WithTimeout(context.Background(), budget)
	defer stopCancel()
	start := time.Now()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if took := time.Since(start); took > budget {
		t.Fatalf("stop with a 5s drain pause took %s, longer than its budget %s", took, budget)
	}
	if got := time.Duration(left.Load()); got < budget/5 {
		t.Fatalf("the hook after HTTP got %s of the stop budget, want about a third", got)
	}
}

// После паузы дренажа запрос в полёте дорабатывает ещё HTTP_SHUTDOWN_TIMEOUT: пауза не
// съедает время запросов.
func TestModule_StopRequestGetsShutdownTimeoutAfterPause(t *testing.T) {
	t.Setenv("HTTP_SHUTDOWN_DRAIN_DELAY", "1s")
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "1s")
	log := logtest.Capture()
	entered := make(chan struct{})
	routes := fx.Invoke(func(api huma.API) {
		huma.Get(api, "/slow", func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			close(entered)
			select {
			case <-time.After(1500 * time.Millisecond): // пауза и половина HTTP_SHUTDOWN_TIMEOUT
				return nil, nil // тело не нужно
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	})
	var srv *httpkit.Server
	app := fx.New(fx.NopLogger, fx.Supply(testConfig(t, "127.0.0.1:0")),
		fx.Provide(func() logkit.Logger { return log }), routes, httpkit.Module, fx.Populate(&srv))
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	status := make(chan int, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+srv.Addr().String()+"/slow", nil)
		if err != nil {
			status <- 0
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		_ = resp.Body.Close()
		status <- resp.StatusCode
	}()
	<-entered

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second) // ⅔ — больше паузы и таймаута
	defer stopCancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := <-status; got != http.StatusNoContent {
		t.Fatalf("request finishing within HTTP_SHUTDOWN_TIMEOUT after the pause: status %d, want 204", got)
	}
	if w, ok := log.Find(slog.LevelWarn, "http server stop deadline: requests cancelled, connections closed"); ok {
		t.Fatalf("stop deadline hit before the pause and HTTP_SHUTDOWN_TIMEOUT ran out: %+v", w)
	}
}

// Регрессия KTST-11: HTTP_SHUTDOWN_TIMEOUT — свой предел остановки HTTP. Запрос, который не
// завершается, обрывается через него, а не держит остановку весь бюджет fx.
func TestModule_StopIsBoundedByShutdownTimeout(t *testing.T) {
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "200ms")
	log := logtest.Capture()
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	routes := fx.Invoke(func(api huma.API) {
		huma.Get(api, "/stuck", func(context.Context, *struct{}) (*struct{}, error) {
			close(entered)
			<-release
			return nil, nil // тело не нужно
		})
	})
	var srv *httpkit.Server
	app := fx.New(fx.NopLogger, fx.Supply(testConfig(t, "127.0.0.1:0")),
		fx.Provide(func() logkit.Logger { return log }), routes, httpkit.Module, fx.Populate(&srv))
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+srv.Addr().String()+"/stuck", nil)
		if err != nil {
			return
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 6*time.Second) // ⅔ бюджета — 4 с
	defer stopCancel()
	start := time.Now()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("stop took %s: a stuck request is cut at HTTP_SHUTDOWN_TIMEOUT (200ms), not at the fx budget", took)
	}
	if w, ok := log.Find(slog.LevelWarn, "http server stop deadline: requests cancelled, connections closed"); !ok || w.Str("requests") != "1" {
		t.Fatalf("stop deadline WARN with the cancelled request: %+v %v", w, log.Entries())
	}
}

// Запрос дольше остановки не съедает бюджет fx: на дедлайне HTTP — две трети остатка — ctx
// хендлеров отменяется (и у захваченного соединения, которое Close не закрывает: WebSocket),
// соединения закрываются (и у хендлера, который ctx не слушает), а хук после HTTP получает
// остаток; WARN называет прерванные запросы.
func TestModule_StopDeadlineCancelsHandlersAndLeavesBudget(t *testing.T) {
	log := logtest.Capture()
	entered, stuck, hijacked, release := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	var cancelled, hijackCancelled atomic.Bool
	var left atomic.Int64
	later := fx.Module("later", fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{OnStop: func(ctx context.Context) error {
			if deadline, ok := ctx.Deadline(); ok {
				left.Store(int64(time.Until(deadline)))
			}
			return nil
		}})
	}))
	routes := fx.Invoke(func(s *httpkit.Server, api huma.API) {
		s.Router().Get("/ws", func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			close(hijacked)
			<-r.Context().Done()
			hijackCancelled.Store(true)
		})
		huma.Get(api, "/stream", func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			close(entered)
			<-ctx.Done()
			cancelled.Store(true)
			return nil, ctx.Err()
		})
		huma.Get(api, "/stuck", func(context.Context, *struct{}) (*struct{}, error) {
			close(stuck)
			<-release
			return nil, nil // тело не нужно
		})
	})
	var srv *httpkit.Server
	app := fx.New(fx.NopLogger, fx.Supply(testConfig(t, "127.0.0.1:0")),
		fx.Provide(func() logkit.Logger { return log }), later, routes, httpkit.Module, fx.Populate(&srv))
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	call := func(path string, done chan<- error) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+srv.Addr().String()+path, nil)
		if err == nil {
			var resp *http.Response
			if resp, err = http.DefaultClient.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
		done <- err
	}
	streamDone, stuckDone := make(chan error, 1), make(chan error, 1)
	go call("/stream", streamDone)
	go call("/stuck", stuckDone)
	ws, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	if _, err := ws.Write([]byte("GET /ws HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	<-entered
	<-stuck
	<-hijacked

	const budget = 1500 * time.Millisecond // HTTP_SHUTDOWN_TIMEOUT по умолчанию больше
	stopCtx, stopCancel := context.WithTimeout(context.Background(), budget)
	defer stopCancel()
	start := time.Now()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if took := time.Since(start); took > budget {
		t.Fatalf("stop took %s, longer than its budget", took)
	}
	if got := time.Duration(left.Load()); got < budget/5 {
		t.Fatalf("the hook after HTTP got %s of the stop budget, want about a third", got)
	}
	for wait := time.Now().Add(time.Second); (!cancelled.Load() || !hijackCancelled.Load()) && time.Now().Before(wait); {
		time.Sleep(10 * time.Millisecond)
	}
	if !cancelled.Load() || !hijackCancelled.Load() {
		t.Fatalf("handler ctx cancelled at the stop deadline: plain %v, hijacked %v", cancelled.Load(), hijackCancelled.Load())
	}
	select {
	case err := <-stuckDone:
		if err == nil {
			t.Fatal("a request ignoring ctx got a response after the stop deadline, want its connection closed")
		}
	case <-time.After(time.Second):
		t.Fatal("connection of a request ignoring ctx stays open after Stop")
	}
	w, ok := log.Find(slog.LevelWarn, "http server stop deadline: requests cancelled, connections closed")
	if !ok || w.Str("requests") != "3" {
		t.Fatalf("stop deadline WARN with the number of cancelled requests: %+v %v", w, log.Entries())
	}
	<-streamDone
}
