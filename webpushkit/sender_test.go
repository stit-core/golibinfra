package webpushkit_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/webpushkit"
)

func newSubscription(t *testing.T, endpoint string) webpushkit.Subscription {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ua key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("auth: %v", err)
	}
	return webpushkit.Subscription{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}
}

func newSender(t *testing.T, client *http.Client) *webpushkit.Sender {
	t.Helper()
	pub, priv, err := webpushkit.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys: %v", err)
	}
	s, err := webpushkit.NewSender(webpushkit.Config{
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
		Subject:         "mailto:ops@stit.tech",
		DefaultTTL:      12 * time.Hour,
	}, client)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	return s
}

func TestSend_Success_Headers(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	s := newSender(t, srv.Client())
	sub := newSubscription(t, srv.URL+"/push/xyz")

	err := s.Send(
		context.Background(), sub, []byte(`{"phase":"break"}`),
		webpushkit.WithTTL(60*time.Second),
		webpushkit.WithUrgency(webpushkit.UrgencyHigh),
		webpushkit.WithTopic("pomodoro"),
	)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if a := got.Header.Get("Authorization"); !strings.HasPrefix(a, "vapid t=") || !strings.Contains(a, ", k=") {
		t.Fatalf("Authorization=%q", a)
	}
	if ce := got.Header.Get("Content-Encoding"); ce != "aes128gcm" {
		t.Fatalf("Content-Encoding=%q", ce)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type=%q", ct)
	}
	if ttl := got.Header.Get("TTL"); ttl != "60" {
		t.Fatalf("TTL=%q, want 60", ttl)
	}
	if u := got.Header.Get("Urgency"); u != "high" {
		t.Fatalf("Urgency=%q", u)
	}
	if tp := got.Header.Get("Topic"); tp != "pomodoro" {
		t.Fatalf("Topic=%q", tp)
	}
	// Тело — заголовок aes128gcm (salt 16 || rs 4 || idlen 1 || pubkey 65) + шифртекст.
	if len(body) <= 86 {
		t.Fatalf("body too short: %d bytes", len(body))
	}
}

func TestSend_DefaultTTL(t *testing.T) {
	var ttl string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ttl = r.Header.Get("TTL")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	s := newSender(t, srv.Client())
	if err := s.Send(context.Background(), newSubscription(t, srv.URL), []byte("x")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ttl != "43200" { // 12h
		t.Fatalf("default TTL=%q, want 43200", ttl)
	}
}

func TestSend_StatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusOK, nil},
		{http.StatusCreated, nil},
		{http.StatusAccepted, nil},
		{http.StatusNotFound, webpushkit.ErrSubscriptionGone},
		{http.StatusGone, webpushkit.ErrSubscriptionGone},
		{http.StatusRequestEntityTooLarge, webpushkit.ErrPayloadTooLarge},
		{http.StatusTooManyRequests, webpushkit.ErrPush},
		{http.StatusInternalServerError, webpushkit.ErrPush},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			s := newSender(t, srv.Client())
			err := s.Send(context.Background(), newSubscription(t, srv.URL), []byte("hi"))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("status %d: want nil, got %v", tc.status, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("status %d: want %v, got %v", tc.status, tc.want, err)
			}
		})
	}
}

func TestSend_PushErrorCarriesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	s := newSender(t, srv.Client())
	err := s.Send(context.Background(), newSubscription(t, srv.URL), []byte("hi"))

	var pe *webpushkit.PushError
	if !errors.As(err, &pe) {
		t.Fatalf("want *PushError, got %T", err)
	}
	if pe.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("StatusCode=%d", pe.StatusCode)
	}
	if pe.RetryAfter != "120" {
		t.Fatalf("RetryAfter=%q", pe.RetryAfter)
	}
}

func TestSend_PayloadTooLarge(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	s := newSender(t, srv.Client())
	err := s.Send(context.Background(), newSubscription(t, srv.URL), make([]byte, 4000))
	if !errors.Is(err, webpushkit.ErrPayloadTooLarge) {
		t.Fatalf("want ErrPayloadTooLarge, got %v", err)
	}
	if called {
		t.Fatal("превышающий лимит payload не должен уходить на push-сервис")
	}
}

func TestSend_InvalidSubscription(t *testing.T) {
	s := newSender(t, http.DefaultClient)
	sub := webpushkit.Subscription{Endpoint: "https://push.example/ep", P256dh: "!!!bad", Auth: "AAAA"}
	if err := s.Send(context.Background(), sub, []byte("x")); !errors.Is(err, webpushkit.ErrSubscription) {
		t.Fatalf("want ErrSubscription, got %v", err)
	}
}
