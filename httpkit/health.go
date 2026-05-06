package httpkit

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

type Probe func(ctx context.Context) error

type healthBody struct {
	Status string `json:"status"`
}

type healthOutput struct {
	Body healthBody
}

func RegisterHealth(api huma.API, probes ...Probe) {
	huma.Register(api, huma.Operation{
		OperationID: "get-health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check",
		Security:    []map[string][]string{},
	}, func(ctx context.Context, _ *struct{}) (*healthOutput, error) {
		return &healthOutput{Body: healthBody{Status: "ok"}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-ready",
		Method:      http.MethodGet,
		Path:        "/ready",
		Summary:     "Readiness check",
		Security:    []map[string][]string{},
	}, func(ctx context.Context, _ *struct{}) (*healthOutput, error) {
		var errs []error
		for _, p := range probes {
			if err := p(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return nil, huma.Error503ServiceUnavailable("unhealthy", errs...)
		}
		return &healthOutput{Body: healthBody{Status: "ok"}}, nil
	})
}
