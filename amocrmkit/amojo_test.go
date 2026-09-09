package amocrmkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Каждый amoJo-вызов подписан и уходит по своему адресу.
func TestAmojoWireContracts(t *testing.T) {
	const secret = "sekret"
	tgt := AmojoTarget{TLD: "ru", AccountKey: "demo", Secret: secret}
	ctx := context.Background()

	cases := []struct {
		name string
		want string
		resp string
		do   func(c *Client) error
	}{
		{
			name: "disconnect", want: "DELETE /v2/origin/custom/chan-1/disconnect",
			do: func(c *Client) error { return c.AmojoDisconnect(ctx, tgt, "chan-1", "amojo-id") },
		},
		{
			name: "create chat", want: "POST /v2/origin/custom/scope-1/chats", resp: `{"id":"conv-ref"}`,
			do: func(c *Client) error {
				out, err := c.AmojoCreateChat(ctx, tgt, "scope-1", CreateChatInput{
					ConversationID: "c-1", User: AmojoParticipant{ID: "u1", Name: "К"},
				})
				if err == nil && out.ID != "conv-ref" {
					t.Errorf("chat = %+v", out)
				}
				return err
			},
		},
		{
			name: "edit message", want: "POST /v2/origin/custom/scope-1", resp: `{"new_message":{"msgid":"m"}}`,
			do: func(c *Client) error {
				_, err := c.AmojoEditMessage(ctx, tgt, "scope-1", NewMessagePayload{MsgID: "m"})
				return err
			},
		},
		{
			name: "react", want: "POST /v2/origin/custom/scope-1/react",
			do: func(c *Client) error {
				return c.AmojoReact(ctx, tgt, "scope-1", map[string]any{"type": "react", "emoji": "👍"})
			},
		},
		{
			name: "delivery status", want: "POST /v2/origin/custom/scope-1/msg-1/delivery_status",
			do: func(c *Client) error {
				return c.AmojoDeliveryStatus(ctx, tgt, "scope-1", "msg-1", map[string]any{"delivery_status": 1})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Method + " " + r.URL.Path
				raw, _ := io.ReadAll(r.Body)
				assertAmojoSignature(t, r, raw, secret)
				_, _ = w.Write([]byte(tc.resp))
			}))
			defer srv.Close()

			if err := tc.do(newTestClient(t, srv.URL)); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got != tc.want {
				t.Errorf("wire = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListParamsValues(t *testing.T) {
	p := ListParams{
		Page: 2, Limit: 50, With: []string{"contacts", "companies"}, Query: "иванов",
		Filter: map[string]string{"id": "7"}, Order: map[string]string{"updated_at": "desc"},
	}
	got := p.values().Encode()
	want := "filter%5Bid%5D=7&limit=50&order%5Bupdated_at%5D=desc&page=2&query=%D0%B8%D0%B2%D0%B0%D0%BD%D0%BE%D0%B2&with=contacts%2Ccompanies"
	if got != want {
		t.Errorf("values = %q, want %q", got, want)
	}
	if len(ListParams{}.values()) != 0 {
		t.Error("empty params must not produce a query")
	}
}

// WithTransport подставляет базовый транспорт собираемого китом httpclient.
func TestWithTransport(t *testing.T) {
	var hits int
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"_embedded":{"leads":[]}}`)),
			Header:     http.Header{},
			Request:    r,
		}, nil
	})
	c := newTestClientCfg(t, Config{RateRPS: 1000}, testHTTPConfig(), WithTransport(rt))
	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err != nil {
		t.Fatalf("ListLeads: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
