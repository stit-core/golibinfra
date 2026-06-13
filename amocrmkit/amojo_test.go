package amocrmkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// assertAmojoSignature пересчитывает исходящую 5-компонентную подпись по принятым
// заголовкам/телу и сверяет с X-Signature — проверяет, что клиент подписал верно.
func assertAmojoSignature(t *testing.T, r *http.Request, body []byte, secret string) {
	t.Helper()
	date := r.Header.Get("Date")
	if date == "" {
		t.Error("missing Date header")
	}
	if r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
	}
	wantMD5 := md5Hex(body)
	if r.Header.Get("Content-MD5") != wantMD5 {
		t.Errorf("Content-MD5 = %q, want %q", r.Header.Get("Content-MD5"), wantMD5)
	}
	signingString := r.Method + "\n" + wantMD5 + "\napplication/json\n" + date + "\n" + r.URL.Path
	wantSig := hmacSHA1Hex([]byte(signingString), secret)
	if r.Header.Get("X-Signature") != wantSig {
		t.Errorf("X-Signature = %q, want %q", r.Header.Get("X-Signature"), wantSig)
	}
}

func TestAmojoConnect_SignsAndParsesScope(t *testing.T) {
	const secret = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	var gotBody ConnectInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/origin/custom/chan-id/connect" {
			t.Errorf("path = %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		assertAmojoSignature(t, r, raw, secret)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"scope_id":"chan-id_amojo-id"}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	tgt := AmojoTarget{TLD: "ru", AccountKey: "demo", Secret: secret}
	res, err := c.AmojoConnect(context.Background(), tgt, "chan-id", ConnectInput{AccountID: "amojo-id", Title: "T"})
	if err != nil {
		t.Fatalf("AmojoConnect: %v", err)
	}
	if res.ScopeID != "chan-id_amojo-id" {
		t.Errorf("scope_id = %q", res.ScopeID)
	}
	if gotBody.HookAPIVersion != "v2" {
		t.Errorf("hook_api_version should default to v2, got %q", gotBody.HookAPIVersion)
	}
}

func TestAmojoNewMessage_EnvelopeAndResult(t *testing.T) {
	const secret = "sekret"
	var env struct {
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/origin/custom/scope-1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		assertAmojoSignature(t, r, raw, secret)
		_ = json.Unmarshal(raw, &env)
		_, _ = w.Write([]byte(`{"new_message":{"msgid":"amo-msg","ref_id":"my-msg"}}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	tgt := AmojoTarget{TLD: "ru", AccountKey: "demo", Secret: secret}
	res, err := c.AmojoNewMessage(context.Background(), tgt, "scope-1", NewMessagePayload{
		MsgID:   "my-msg",
		Silent:  true,
		Sender:  AmojoParticipant{ID: "u1", Name: "Client"},
		Message: map[string]any{"type": "text", "text": "hi"},
	})
	if err != nil {
		t.Fatalf("AmojoNewMessage: %v", err)
	}
	if env.EventType != "new_message" {
		t.Errorf("event_type = %q", env.EventType)
	}
	if res.NewMessage.MsgID != "amo-msg" || res.NewMessage.RefID != "my-msg" {
		t.Errorf("result = %+v", res.NewMessage)
	}
}

func TestAmojoTyping_NoBodyDecode(t *testing.T) {
	const secret = "s"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		assertAmojoSignature(t, r, raw, secret)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	tgt := AmojoTarget{TLD: "ru", AccountKey: "demo", Secret: secret}
	if err := c.AmojoTyping(context.Background(), tgt, "scope-1", map[string]any{"conversation_id": "c"}); err != nil {
		t.Fatalf("AmojoTyping: %v", err)
	}
}
