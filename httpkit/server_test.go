package httpkit

import (
	"context"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

func TestBindAPI_NoSchemes(t *testing.T) {
	s := NewServer(Config{APITitle: "t", APIVersion: "0"})
	api := s.BindAPI()
	if api.OpenAPI().Components.SecuritySchemes != nil {
		t.Fatalf("expected nil SecuritySchemes when no entries provided, got %v", api.OpenAPI().Components.SecuritySchemes)
	}
}

func TestBindAPI_WithSchemes(t *testing.T) {
	cookie := SecuritySchemeEntry{
		Name:   "Cookie",
		Scheme: &huma.SecurityScheme{Type: "apiKey", In: "cookie", Name: "sid"},
	}
	bearer := SecuritySchemeEntry{
		Name:   "Bearer",
		Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
	}

	s := NewServer(Config{APITitle: "t", APIVersion: "0"}, cookie, bearer)
	api := s.BindAPI()

	got := api.OpenAPI().Components.SecuritySchemes
	if len(got) != 2 {
		t.Fatalf("want 2 schemes, got %d (%v)", len(got), got)
	}
	if got["Cookie"] != cookie.Scheme || got["Bearer"] != bearer.Scheme {
		t.Errorf("scheme mismatch: %+v", got)
	}
}

func TestBindAPI_Idempotent(t *testing.T) {
	s := NewServer(Config{APITitle: "t", APIVersion: "0"})
	if s.BindAPI() != s.BindAPI() {
		t.Fatal("BindAPI must return same api on repeat call")
	}
}

func TestServer_ListenAddrAndStop(t *testing.T) {
	s := NewServer(Config{Addr: "127.0.0.1:0"})
	if s.Addr() != nil {
		t.Fatal("Addr before Listen must be nil")
	}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("Listen must be idempotent: %v", err)
	}
	if s.Addr() == nil {
		t.Fatal("Addr after Listen")
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve after Stop must return nil, got %v", err)
	}
}
