package telemetry

import "testing"

func TestOTLPHeaders(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cfg  Config
		want map[string]string
	}{
		"empty token returns nil": {
			cfg:  Config{},
			want: nil,
		},
		"token produces bearer header": {
			cfg:  Config{OTLPBearerToken: "abc123"},
			want: map[string]string{"Authorization": "Bearer abc123"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := otlpHeaders(tc.cfg)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d headers, want %d", len(got), len(tc.want))
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("header %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestInitDisabled(t *testing.T) {
	t.Parallel()

	p, err := Init(t.Context(), Config{Disabled: true})
	if err != nil {
		t.Fatalf("Init(disabled) error: %v", err)
	}
	if p.TracerProvider == nil || p.MeterProvider == nil || p.LoggerProvider == nil {
		t.Fatal("Init(disabled) должен вернуть non-nil провайдеры (noop, без экспортёров)")
	}
	if err := p.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown(disabled) error: %v", err)
	}
}

func TestInitRequiresServiceName(t *testing.T) {
	t.Parallel()

	if _, err := Init(t.Context(), Config{}); err == nil {
		t.Fatal("Init без ServiceName при включённой телеметрии обязан вернуть ошибку")
	}
}
