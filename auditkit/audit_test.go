package auditkit_test

import (
	"context"
	"encoding/json"
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
		{name: "NUL in actor", entry: auditkit.Entry{ActorID: "u\x00", Action: "a", Resource: "r"}, wantErr: true},
		{name: "NUL in action", entry: auditkit.Entry{Action: "a\x00", Resource: "r"}, wantErr: true},
		{name: "NUL in resource", entry: auditkit.Entry{Action: "a", Resource: "r\x00"}, wantErr: true},
		{name: "NUL in resource id", entry: auditkit.Entry{Action: "a", Resource: "r", ResourceID: "42\x00"}, wantErr: true},
		{name: "NUL in IP", entry: auditkit.Entry{Action: "a", Resource: "r", IP: "\x00"}, wantErr: true},
		{name: "NUL in request id", entry: auditkit.Entry{Action: "a", Resource: "r", RequestID: "req\x00"}, wantErr: true},
		{name: "bad UTF-8 in actor", entry: auditkit.Entry{ActorID: "\xff\xfe", Action: "a", Resource: "r"}, wantErr: true},
		{name: "bad UTF-8 in action", entry: auditkit.Entry{Action: "\xff", Resource: "r"}, wantErr: true},
		{name: "bad UTF-8 in resource", entry: auditkit.Entry{Action: "a", Resource: "\xff"}, wantErr: true},
		{name: "bad UTF-8 in resource id", entry: auditkit.Entry{Action: "a", Resource: "r", ResourceID: "\xff"}, wantErr: true},
		{name: "bad UTF-8 in IP", entry: auditkit.Entry{Action: "a", Resource: "r", IP: "\xff"}, wantErr: true},
		{name: "bad UTF-8 in request id", entry: auditkit.Entry{Action: "a", Resource: "r", RequestID: "\xff"}, wantErr: true},
		{name: "NUL in metadata value", entry: auditkit.Entry{Action: "a", Resource: "r", Metadata: map[string]any{"name": "bob\x00"}}, wantErr: true},
		{name: "NUL in metadata key", entry: auditkit.Entry{Action: "a", Resource: "r", Metadata: map[string]any{"\x00": "v"}}, wantErr: true},
		{name: "metadata without JSON form", entry: auditkit.Entry{Action: "a", Resource: "r", Metadata: map[string]any{"c": make(chan int)}}, wantErr: true},
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

// Metadata — то, что сохранит Postgres JSONB: \u0000 и сырые не-UTF-8 байты он отвергает, а
// предел держит Metadata контекстом события, а не дампом payload'а.
func TestEntry_MetadataJSON(t *testing.T) {
	t.Parallel()
	entry := func(m map[string]any) auditkit.Entry { return auditkit.Entry{Action: "a", Resource: "r", Metadata: m} }
	// {"k":"…"} — 8 байт обвязки вокруг значения.
	atLimit := strings.Repeat("x", auditkit.MaxMetadataBytes-8)

	for name, c := range map[string]struct {
		metadata map[string]any
		want     string
		wantErr  bool
	}{
		"empty is NULL":              {metadata: map[string]any{}},
		"plain":                      {metadata: map[string]any{"k": "v"}, want: `{"k":"v"}`},
		"at the limit":               {metadata: map[string]any{"k": atLimit}, want: `{"k":"` + atLimit + `"}`},
		"over the limit":             {metadata: map[string]any{"k": atLimit + "x"}, wantErr: true},
		"NUL in a string":            {metadata: map[string]any{"k": "a\x00b"}, wantErr: true},
		"NUL in raw JSON":            {metadata: map[string]any{"k": json.RawMessage(`"\u0000"`)}, wantErr: true},
		"escaped slash before NUL":   {metadata: map[string]any{"k": json.RawMessage(`"\\\u0000"`)}, wantErr: true},
		"NUL after escaped u0000":    {metadata: map[string]any{"k": json.RawMessage(`"\\u0000\u0000"`)}, wantErr: true},
		"escaped slash and u0000":    {metadata: map[string]any{"k": `\u0000`}, want: `{"k":"\\u0000"}`},
		"two NUL-free escapes":       {metadata: map[string]any{"k": json.RawMessage(`"\\u0000\\\\u0000"`)}, want: `{"k":"\\u0000\\\\u0000"}`},
		"raw bytes not UTF-8":        {metadata: map[string]any{"k": json.RawMessage("\"\xff\"")}, wantErr: true},
		"string not UTF-8 is U+FFFD": {metadata: map[string]any{"k": "a\xffb"}, want: `{"k":"a\ufffdb"}`},
		"nested NUL":                 {metadata: map[string]any{"k": []any{map[string]any{"n": "\x00"}}}, wantErr: true},
	} {
		got, err := entry(c.metadata).MetadataJSON()
		if c.wantErr {
			if !errors.Is(err, auditkit.ErrInvalidEntry) {
				t.Errorf("%s: MetadataJSON() = %q, %v; want ErrInvalidEntry", name, got, err)
			}
			if verr := entry(c.metadata).Validate(); !errors.Is(verr, auditkit.ErrInvalidEntry) {
				t.Errorf("%s: Validate() = %v, want ErrInvalidEntry", name, verr)
			}
			continue
		}
		if err != nil || string(got) != c.want {
			t.Errorf("%s: MetadataJSON() = %q, %v; want %q", name, got, err, c.want)
		}
	}

	if got, err := (auditkit.Entry{Resource: "r", Metadata: map[string]any{"k": "v"}}).MetadataJSON(); got != nil || !errors.Is(err, auditkit.ErrInvalidEntry) {
		t.Errorf("MetadataJSON of an entry without Action = %q, %v; want ErrInvalidEntry", got, err)
	}
}
