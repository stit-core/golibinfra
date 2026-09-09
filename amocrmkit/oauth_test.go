package amocrmkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestExchangeCode_ParsesTokens(t *testing.T) {
	var gotBody oauthReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth2/access_token" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"token_type":"Bearer","expires_in":86400,"server_time":1751621727,
			"access_token":"acc-jwt","refresh_token":"ref-tok"
		}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	tok, err := c.ExchangeCode(context.Background(), testAccount(), testOAuthConfig(), "the-code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}

	if gotBody.GrantType != "authorization_code" {
		t.Errorf("grant_type = %q, want authorization_code", gotBody.GrantType)
	}
	if gotBody.Code != "the-code" || gotBody.RedirectURI != "https://cb" {
		t.Errorf("body code/redirect mismatch: %+v", gotBody)
	}
	if gotBody.ClientID != "cid" || gotBody.ClientSecret != "csecret" {
		t.Errorf("body credentials mismatch: %+v", gotBody)
	}
	if tok.AccessToken != "acc-jwt" || tok.RefreshToken != "ref-tok" {
		t.Errorf("tokens = %+v", tok)
	}
	if tok.ExpiresIn != 86400 || tok.ServerTime != 1751621727 {
		t.Errorf("ttl fields = %+v", tok)
	}
	if tok.ExpiresAt().Unix() != 1751621727+86400 {
		t.Errorf("ExpiresAt = %d, want %d", tok.ExpiresAt().Unix(), 1751621727+86400)
	}
}

func TestRefreshToken_GrantAndBody(t *testing.T) {
	var gotBody oauthReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":86400,"server_time":1,"access_token":"a2","refresh_token":"r2"}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	tok, err := c.RefreshToken(context.Background(), testAccount(), testOAuthConfig(), "old-refresh")
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if gotBody.GrantType != "refresh_token" || gotBody.RefreshToken != "old-refresh" {
		t.Errorf("refresh body = %+v", gotBody)
	}
	if gotBody.RedirectURI != "https://cb" {
		t.Error("redirect_uri must be present in refresh body (wire requirement)")
	}
	if tok.AccessToken != "a2" {
		t.Errorf("token = %+v", tok)
	}
}

// Без server_time TTL считался бы от 1970 и потребитель рефрешил бы по кругу.
func TestRefreshToken_MissingServerTimeFallsBackToClock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":86400,"access_token":"a","refresh_token":"r"}`))
	}))
	t.Cleanup(srv.Close)

	now := time.Unix(1_800_000_000, 0)
	clk := clockkit.NewTickable(now)
	c, err := New(Config{RateRPS: 1000, HostOverride: srv.URL}, testHTTPConfig(), clk, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tok, err := c.RefreshToken(context.Background(), testAccount(), testOAuthConfig(), "r0")
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if tok.ServerTime != now.Unix() {
		t.Errorf("ServerTime = %d, want %d (clock)", tok.ServerTime, now.Unix())
	}
	if got := tok.ExpiresAt().Unix(); got != now.Unix()+86400 {
		t.Errorf("ExpiresAt = %d, want %d", got, now.Unix()+86400)
	}
}

func TestOAuth_ErrorStatusClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"title":"Unauthorized","status":401,"detail":"bad client"}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	_, err := c.RefreshToken(context.Background(), testAccount(), testOAuthConfig(), "r")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

// Пустой ответ на обмен токенов — не «нулевая пара», а ErrNotFound: молча
// сохранить пустую пару опаснее, чем упасть.
func TestOAuth_EmptyBodyIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	if _, err := c.ExchangeCode(context.Background(), testAccount(), testOAuthConfig(), "code"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
