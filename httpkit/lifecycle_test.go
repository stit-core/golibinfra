package httpkit_test

import (
	"context"
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
		return nil, nil //nolint:nilnil // тело ответа не нужно
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
