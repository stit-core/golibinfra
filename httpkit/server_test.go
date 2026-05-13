package httpkit

import (
	"testing"

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
	if got["Cookie"] != cookie.Scheme {
		t.Errorf("Cookie scheme mismatch: %+v", got["Cookie"])
	}
	if got["Bearer"] != bearer.Scheme {
		t.Errorf("Bearer scheme mismatch: %+v", got["Bearer"])
	}
}

func TestBindAPI_Idempotent(t *testing.T) {
	s := NewServer(Config{APITitle: "t", APIVersion: "0"})
	a := s.BindAPI()
	b := s.BindAPI()
	if a != b {
		t.Fatal("BindAPI must return same api on repeat call")
	}
}
