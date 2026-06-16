package sberaikit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// TestTokenCachedAndRefreshed — токен минтится один раз и переиспользуется, пока
// не истёк (со скосом); после Advance за expires_at — рефреш.
func TestTokenCachedAndRefreshed(t *testing.T) {
	var mints int64
	start := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	clk := clockkit.NewTickable(start)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&mints, 1)
		if r.Header.Get("RqUID") == "" {
			t.Errorf("oauth: missing RqUID")
		}
		// expires_at = текущее время фейк-часов + 30 мин (epoch ms).
		exp := start.Add(30 * time.Minute).UnixMilli()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresAt: exp})
	}))
	defer srv.Close()

	cfg := Config{AuthKey: "a2V5", OAuthURL: srv.URL, Scope: "GIGACHAT_API_PERS"}
	hc := srv.Client()
	tm := newTokenManager(cfg, hc, clk)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		tok, err := tm.token(ctx)
		if err != nil || tok != "tok" {
			t.Fatalf("token() = %q, %v", tok, err)
		}
	}
	if got := atomic.LoadInt64(&mints); got != 1 {
		t.Fatalf("mints = %d, want 1 (cached)", got)
	}

	// Перематываем за expires_at → рефреш.
	clk.Advance(31 * time.Minute)
	if _, err := tm.token(ctx); err != nil {
		t.Fatalf("token() after expiry: %v", err)
	}
	if got := atomic.LoadInt64(&mints); got != 2 {
		t.Fatalf("mints = %d, want 2 (refreshed)", got)
	}
}

// TestTokenInvalidateForces401Refresh — invalidate сбрасывает кэш (путь 401-retry).
func TestTokenInvalidateForcesRemint(t *testing.T) {
	var mints int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&mints, 1)
		exp := time.Now().Add(30 * time.Minute).UnixMilli()
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresAt: exp})
	}))
	defer srv.Close()

	tm := newTokenManager(Config{AuthKey: "a2V5", OAuthURL: srv.URL}, srv.Client(), clockkit.Real{})
	ctx := context.Background()
	_, _ = tm.token(ctx)
	tm.invalidate()
	_, _ = tm.token(ctx)
	if got := atomic.LoadInt64(&mints); got != 2 {
		t.Fatalf("mints = %d, want 2 after invalidate", got)
	}
}

// TestDoRequest401RefreshRetry — на 401 кит рефрешит токен и ретраит один раз.
func TestDoRequest401RefreshRetry(t *testing.T) {
	var oauthCalls, chatCalls int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&oauthCalls, 1)
		exp := time.Now().Add(30 * time.Minute).UnixMilli()
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresAt: exp})
	})
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&chatCalls, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"status":401,"message":"expired"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"chat.completion","model":"GigaChat-2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	resp, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("text = %q", resp.Text())
	}
	if atomic.LoadInt64(&chatCalls) != 2 {
		t.Fatalf("chatCalls = %d, want 2 (401 then retry)", chatCalls)
	}
	if atomic.LoadInt64(&oauthCalls) != 2 {
		t.Fatalf("oauthCalls = %d, want 2 (initial + refresh)", oauthCalls)
	}
}

func TestSingleflightToken(t *testing.T) {
	var sf singleflightToken
	var calls int64
	const n = 20
	results := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			tok, _ := sf.do(func() (string, error) {
				atomic.AddInt64(&calls, 1)
				time.Sleep(10 * time.Millisecond)
				return "v", nil
			})
			results <- tok
		}()
	}
	for i := 0; i < n; i++ {
		if r := <-results; r != "v" {
			t.Fatalf("result = %q", r)
		}
	}
	// Не строго 1 (зависит от планировщика), но заметно меньше n — single-flight схлопывает.
	if c := atomic.LoadInt64(&calls); c >= n {
		t.Fatalf("singleflight did not coalesce: %d calls for %d goroutines", c, n)
	}
}
