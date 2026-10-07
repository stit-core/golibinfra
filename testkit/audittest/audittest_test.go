package audittest_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/audittest"
)

var _ auditkit.Logger = (*audittest.Recorder)(nil)

func TestRecorder_RoundTrip(t *testing.T) {
	t.Parallel()
	r := &audittest.Recorder{}
	ctx := context.Background()
	mustLog(t, r, auditkit.Entry{Action: "login", Resource: "auth", ActorID: "u1"})
	mustLog(t, r, auditkit.Entry{Action: "register", Resource: "user", ActorID: "u1"})
	mustLog(t, r, auditkit.Entry{Action: "login", Resource: "auth", ActorID: "u2"})

	if got := r.Actions(); len(got) != 3 || got[0] != "login" || got[2] != "login" {
		t.Fatalf("Actions=%v", got)
	}
	if r.Last("login") == nil || r.Last("login").ActorID != "u2" {
		t.Fatalf("Last(login)=%+v", r.Last("login"))
	}
	if r.Last("missing") != nil {
		t.Fatalf("Last(missing) must be nil")
	}
	if got := r.ByResource("user"); len(got) != 1 || got[0].Action != "register" {
		t.Fatalf("ByResource(user)=%+v", got)
	}
	r.Reset()
	if len(r.Entries()) != 0 {
		t.Fatalf("Reset did not clear entries")
	}
	_ = ctx
}

func mustLog(t *testing.T, r *audittest.Recorder, e auditkit.Entry) {
	t.Helper()
	if err := r.Log(context.Background(), e); err != nil {
		t.Fatalf("log: %v", err)
	}
}

func TestRecorder_HasAndEntriesCopy(t *testing.T) {
	t.Parallel()
	r := &audittest.Recorder{}
	mustLog(t, r, auditkit.Entry{Action: "login", Resource: "auth"})

	if !r.Has("login") {
		t.Error("Has(login) = false")
	}
	if r.Has("logout") {
		t.Error("Has(logout) = true, want false")
	}

	entries := r.Entries()
	entries[0].Action = "tampered"
	if r.Entries()[0].Action != "login" {
		t.Error("Entries must return a copy of the slice")
	}
}

// Паритет с pgstore: фейк отвергает те же записи, что и Store.
func TestRecorder_RejectsInvalidEntry(t *testing.T) {
	t.Parallel()
	r := &audittest.Recorder{}
	for _, e := range []auditkit.Entry{
		{},
		{Action: "login"},
		{Resource: "auth"},
		{Action: "login", Resource: "auth", ResourceID: "42\x00"},
		{Action: "login", Resource: "auth", ActorID: "\xff"},
		{Action: "login", Resource: "auth", Metadata: map[string]any{"name": "bob\x00"}},
		{Action: "login", Resource: "auth", Metadata: map[string]any{"name": "bob\xff"}},
		{Action: "login", Resource: "auth", Metadata: map[string]any{"k": strings.Repeat("v", auditkit.MaxMetadataBytes)}},
	} {
		if err := r.Log(context.Background(), e); !errors.Is(err, auditkit.ErrInvalidEntry) {
			t.Fatalf("Log(%.80v) = %v, want ErrInvalidEntry", e, err)
		}
	}
	if len(r.Entries()) != 0 {
		t.Fatalf("invalid entries must not be recorded: %+v", r.Entries())
	}
}

// Метаданные копируются на входе и на выходе: мутация чужой map не переписывает
// историю, как не переписывает её pgstore (он маршалит map сразу).
func TestRecorder_ClonesMetadata(t *testing.T) {
	t.Parallel()
	r := &audittest.Recorder{}
	meta := map[string]any{"k": "v"}
	mustLog(t, r, auditkit.Entry{Action: "login", Resource: "auth", Metadata: meta})

	meta["k"] = "tampered"
	if got := r.Entries()[0].Metadata["k"]; got != "v" {
		t.Fatalf("Metadata mutated through caller map: %v", got)
	}

	r.Entries()[0].Metadata["k"] = "tampered"
	r.Last("login").Metadata["k"] = "tampered"
	r.ByResource("auth")[0].Metadata["k"] = "tampered"
	if got := r.Entries()[0].Metadata["k"]; got != "v" {
		t.Fatalf("Metadata mutated through returned entry: %v", got)
	}
}

// Recorder держит контракт pgstore: запись без IP и request-id берёт их из ctx
// запроса, который собрали middleware httpkit, явные значения не трогает.
func TestRecorder_RequestIDAndIPFromHTTPContext(t *testing.T) {
	t.Parallel()
	r := &audittest.Recorder{}
	h := httpkit.RequestID()(httpkit.ClientIP(0, logkit.Nop())(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		mustLogCtx(t, req.Context(), r, auditkit.Entry{Action: "login", Resource: "auth"})
		mustLogCtx(t, req.Context(), r, auditkit.Entry{Action: "manual", Resource: "auth", IP: "10.0.0.1", RequestID: "own"})
	})))
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set(httpkit.HeaderRequestID, "req-42")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := r.Last("login"); got == nil || got.RequestID != "req-42" || got.IP != "192.0.2.10" {
		t.Fatalf("Last(login) = %+v, want RequestID req-42 and IP 192.0.2.10 from the request", got)
	}
	if got := r.Last("manual"); got == nil || got.RequestID != "own" || got.IP != "10.0.0.1" {
		t.Fatalf("Last(manual) = %+v, want explicit values kept", got)
	}
}

func mustLogCtx(t *testing.T, ctx context.Context, r *audittest.Recorder, e auditkit.Entry) {
	t.Helper()
	if err := r.Log(ctx, e); err != nil {
		t.Errorf("Log(%s): %v", e.Action, err)
	}
}
