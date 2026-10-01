package auditkit_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/internal/clientip"
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
)

// IP и request-id кладут в ctx httpkit и grpckit: запись без них берёт значения
// запроса, явные — не трогает.
func TestEntry_Enrich(t *testing.T) {
	t.Parallel()
	ctx := requestid.With(clientip.With(context.Background(), netip.MustParseAddr("::ffff:203.0.113.7")), "req-1")

	got := auditkit.Entry{Action: "a", Resource: "r"}.Enrich(ctx)
	if got.RequestID != "req-1" || got.IP != "203.0.113.7" {
		t.Errorf("Enrich = RequestID %q, IP %q; want req-1 and the canonical 203.0.113.7", got.RequestID, got.IP)
	}
	own := auditkit.Entry{Action: "a", Resource: "r", RequestID: "own", IP: "10.0.0.1"}.Enrich(ctx)
	if own.RequestID != "own" || own.IP != "10.0.0.1" {
		t.Errorf("Enrich overwrote explicit values: RequestID %q, IP %q", own.RequestID, own.IP)
	}
	bare := auditkit.Entry{Action: "a", Resource: "r"}.Enrich(context.Background())
	if bare.RequestID != "" || bare.IP != "" {
		t.Errorf("Enrich without request ctx = RequestID %q, IP %q; want empty", bare.RequestID, bare.IP)
	}
}

func TestEntry_Validate(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 256)

	tests := []struct {
		name    string
		entry   auditkit.Entry
		wantErr bool
	}{
		{name: "minimal", entry: auditkit.Entry{Action: "user.create", Resource: "user"}},
		{name: "full", entry: auditkit.Entry{
			ActorID:    "u-1",
			Action:     "user.create",
			Resource:   "user",
			ResourceID: "u-2",
			Metadata:   map[string]any{"k": "v"},
			At:         time.Unix(1, 0),
			IP:         "203.0.113.7",
			RequestID:  "req-1",
		}},
		{name: "zero", entry: auditkit.Entry{}, wantErr: true},
		{name: "no action", entry: auditkit.Entry{Resource: "user"}, wantErr: true},
		{name: "no resource", entry: auditkit.Entry{Action: "user.create"}, wantErr: true},
		{name: "long actor", entry: auditkit.Entry{ActorID: long, Action: "a", Resource: "r"}, wantErr: true},
		{name: "long action", entry: auditkit.Entry{Action: long, Resource: "r"}, wantErr: true},
		{name: "long resource", entry: auditkit.Entry{Action: "a", Resource: long}, wantErr: true},
		{name: "long resource id", entry: auditkit.Entry{Action: "a", Resource: "r", ResourceID: long}, wantErr: true},
		{name: "at limit", entry: auditkit.Entry{
			ActorID:    strings.Repeat("x", auditkit.MaxActorIDLen),
			Action:     strings.Repeat("x", auditkit.MaxActionLen),
			Resource:   strings.Repeat("x", auditkit.MaxResourceLen),
			ResourceID: strings.Repeat("x", auditkit.MaxResourceIDLen),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.entry.Validate()
			if tt.wantErr {
				if !errors.Is(err, auditkit.ErrInvalidEntry) {
					t.Fatalf("Validate() = %v, want ErrInvalidEntry", err)
				}
				if !strings.HasPrefix(err.Error(), "auditkit: ") {
					t.Fatalf("Validate() = %q, want auditkit: prefix", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}
