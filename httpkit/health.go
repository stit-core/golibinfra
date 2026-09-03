package httpkit

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Probe — alias healthkit.Probe: киты возвращают его, а fx-группа `health`
// совпадает по типу с тем, что читает httpkit.
type Probe = healthkit.Probe

type healthBody struct {
	Status string `json:"status"`
}

type healthOutput struct {
	Body healthBody
}

// RegisterHealth регистрирует /health (liveness) и /ready (readiness по пробам).
// Текст ошибок проб уходит в лог, а не в ответ: там могут быть DSN и адреса.
func RegisterHealth(api huma.API, log logkit.Logger, probes ...Probe) {
	huma.Register(api, huma.Operation{
		OperationID: "get-health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check",
		Security:    []map[string][]string{},
	}, func(context.Context, *struct{}) (*healthOutput, error) {
		return &healthOutput{Body: healthBody{Status: "ok"}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-ready",
		Method:      http.MethodGet,
		Path:        "/ready",
		Summary:     "Readiness check",
		Security:    []map[string][]string{},
	}, func(ctx context.Context, _ *struct{}) (*healthOutput, error) {
		failed := 0
		for i, p := range probes {
			if err := p(ctx); err != nil {
				failed++
				log.Warn(ctx, "readiness probe failed", "probe", i, "err", err)
			}
		}
		if failed > 0 {
			return nil, huma.Error503ServiceUnavailable("unhealthy")
		}
		return &healthOutput{Body: healthBody{Status: "ok"}}, nil
	})
}
