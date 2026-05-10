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

