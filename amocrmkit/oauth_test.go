package amocrmkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
	tok, err := c.ExchangeCode(context.Background(), "demo", "ru", "cid", "csecret", "the-code", "https://cb")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}

	if gotBody.GrantType != "authorization_code" {
		t.Errorf("grant_type = %q, want authorization_code", gotBody.GrantType)
	}
	if gotBody.Code != "the-code" || gotBody.RedirectURI != "https://cb" {
		t.Errorf("body code/redirect mismatch: %+v", gotBody)
	}
	if tok.AccessToken != "acc-jwt" || tok.RefreshToken != "ref-tok" {
		t.Errorf("tokens = %+v", tok)
	}
	if tok.ExpiresIn != 86400 || tok.ServerTime != 1751621727 {
		t.Errorf("ttl fields = %+v", tok)
	}
	// ExpiresAt = ServerTime + ExpiresIn.
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
	tok, err := c.RefreshToken(context.Background(), "demo", "ru", "cid", "csecret", "old-refresh", "https://cb")
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

func TestOAuth_ErrorStatusClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"title":"Unauthorized","status":401,"detail":"bad client"}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	_, err := c.RefreshToken(context.Background(), "demo", "ru", "cid", "bad", "r", "https://cb")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}
