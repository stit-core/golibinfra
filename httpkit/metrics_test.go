package httpkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.stit.tech/stit-infra/golibinfra/httpkit"
)

func TestMetrics_CounterAndHistogram(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test")

	r := chi.NewRouter()
	r.Use(httpkit.Metrics(meter))
	r.Get("/api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/users/123", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	var foundCounter, foundHistogram bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "http_requests_total":
				data, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("http_requests_total is not Sum[int64]")
				}
				for _, dp := range data.DataPoints {
					if dp.Value >= 1 && hasAttr(dp.Attributes, "path", "/api/users/{id}") {
						foundCounter = true
					}
				}
			case "http_request_duration_seconds":
				data, ok := m.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("http_request_duration_seconds is not Histogram[float64]")
				}
				for _, dp := range data.DataPoints {
					if dp.Count >= 1 && hasAttr(dp.Attributes, "path", "/api/users/{id}") {
						foundHistogram = true
					}
				}
			}
		}
	}

	if !foundCounter {
		t.Error("counter http_requests_total not found or incorrect")
	}
	if !foundHistogram {
		t.Error("histogram http_request_duration_seconds not found or incorrect")
	}
}

func hasAttr(set attribute.Set, key, value string) bool {
	v, ok := set.Value(attribute.Key(key))
	return ok && v.AsString() == value
}
