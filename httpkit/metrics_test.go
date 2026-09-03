package httpkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collectRequests(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http_requests_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("http_requests_total is %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				p, _ := dp.Attributes.Value(attribute.Key("path"))
				s, _ := dp.Attributes.Value(attribute.Key("status"))
				out[p.AsString()+" "+s.AsString()] += dp.Value
			}
		}
	}
	return out
}

func TestMetrics_RoutePatternAndUnmatched(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	r := chi.NewRouter()
	r.Use(metricsWith(newInstrumentsWithMeter(provider.Meter("test"))))
	r.Get("/api/users/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, path := range []string{"/api/users/123", "/api/users/456", "/scan/admin.php", "/scan/wp-login"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	got := collectRequests(t, reader)
	if got["/api/users/{id} 200"] != 2 {
		t.Errorf("route pattern label: %v", got)
	}
	if got["unmatched 404"] != 2 {
		t.Errorf("404 scans must collapse into one label: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("unexpected label cardinality: %v", got)
	}
}
