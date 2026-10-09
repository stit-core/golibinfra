package httpkit

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Probe — alias healthkit.Probe: киты возвращают его, а fx-группа `healthkit.probes`
// совпадает по типу с тем, что читает httpkit.
type Probe = healthkit.Probe

type healthBody struct {
	Status string `json:"status"`
}

type healthOutput struct {
	Body healthBody
}

type probeStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type readyBody struct {
	Status string        `json:"status"`
	Probes []probeStatus `json:"probes,omitempty"`
}

type readyOutput struct {
	Status int
	Body   readyBody
}

// RegisterHealth регистрирует публичные (Public) /health (liveness) и /ready (readiness по
// пробам). Пробы выполняются параллельно, каждая — не дольше probeTimeout. Текст ошибок
// проб уходит в лог, а не в ответ: там могут быть DSN и адреса. С начала Server.Stop
// /ready отвечает 503 draining без проб: балансировщик выводит инстанс до закрытия listener'а.
// nil-логгер — logkit.Nop().
func RegisterHealth(api huma.API, probeTimeout time.Duration, log logkit.Logger, probes ...Probe) {
	if log == nil {
		log = logkit.Nop()
	}
	huma.Register(api, Public(huma.Operation{
		OperationID: "get-health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check",
	}), func(context.Context, *struct{}) (*healthOutput, error) {
		return &healthOutput{Body: healthBody{Status: "ok"}}, nil
	})

	huma.Register(api, Public(huma.Operation{
		OperationID: "get-ready",
		Method:      http.MethodGet,
		Path:        "/ready",
		Summary:     "Readiness check",
	}), func(ctx context.Context, _ *struct{}) (*readyOutput, error) {
		if draining(ctx) {
			return &readyOutput{Status: http.StatusServiceUnavailable, Body: readyBody{Status: "draining"}}, nil
		}
		statuses := runProbes(ctx, log, probeTimeout, probes)
		out := &readyOutput{Status: http.StatusOK, Body: readyBody{Status: "ok", Probes: statuses}}
		for _, s := range statuses {
			if s.Status != "ok" {
				out.Status, out.Body.Status = http.StatusServiceUnavailable, "unhealthy"
				break
			}
		}
		return out, nil
	})
}

// runProbes гоняет пробы параллельно: латентность /ready — максимум по пробам,
// а не сумма, иначе одна медленная зависимость валит readiness по таймауту kubelet.
func runProbes(ctx context.Context, log logkit.Logger, timeout time.Duration, probes []Probe) []probeStatus {
	if len(probes) == 0 {
		return nil
	}
	statuses := make([]probeStatus, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Go(func() {
			statuses[i] = probeStatus{Name: p.Name, Status: "ok"}
			err := healthkit.WithTimeout(p, timeout).Run(ctx)
			var panicked *healthkit.PanicError
			switch {
			case errors.As(err, &panicked):
				statuses[i].Status = "down"
				log.Error(ctx, "readiness probe panicked", "probe", p.Name, "panic", panicked.Value, "stack", string(panicked.Stack))
			case err != nil:
				statuses[i].Status = "down"
				log.Warn(ctx, "readiness probe failed", "probe", p.Name, "error", err)
			}
		})
	}
	wg.Wait()
	return statuses
}
