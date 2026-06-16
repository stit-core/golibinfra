package googleaikit

import (
	"errors"
	"net/http"
	"testing"
)

func TestNewRequiresAPIKeyOnAIStudio(t *testing.T) {
	if _, err := New(Config{Backend: "aistudio"}, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewVertexRequiresProjectLocation(t *testing.T) {
	if _, err := New(Config{Backend: "vertex"}, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
	if _, err := New(Config{Backend: "vertex", Project: "p"}, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig (missing location)", err)
	}
}

func TestNewAIStudioOK(t *testing.T) {
	c, err := New(Config{APIKey: "k", DefaultModel: "gemini-2.5-flash"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.DefaultModel != "gemini-2.5-flash" {
		t.Fatalf("default model not set: %q", c.cfg.DefaultModel)
	}
	if c.Raw() == nil {
		t.Fatal("Raw() returned nil sdk")
	}
}

func TestNewVertexOK(t *testing.T) {
	// Кастомный HTTP-клиент несёт auth сам → genai не ищет ADC (network-free unit-тест).
	_, err := New(
		Config{Backend: "vertex", Project: "p", Location: "us-central1"},
		nil,
		WithHTTPClient(&http.Client{}),
	)
	if err != nil {
		t.Fatalf("New vertex: %v", err)
	}
}

func TestConfigIsVertex(t *testing.T) {
	if (Config{Backend: "aistudio"}).isVertex() {
		t.Fatal("aistudio classified as vertex")
	}
	if !(Config{Backend: "vertex"}).isVertex() {
		t.Fatal("vertex not classified as vertex")
	}
}

func TestResponseText(t *testing.T) {
	r := &Response{Content: []Block{
		TextBlock{Text: "a"},
		ThoughtBlock{Text: "x"},
		TextBlock{Text: "b"},
	}}
	if got := r.Text(); got != "ab" {
		t.Fatalf("Text() = %q, want %q", got, "ab")
	}
}

func TestModelOrFallsBackToDefault(t *testing.T) {
	c, err := New(Config{APIKey: "k", DefaultModel: "gemini-2.5-flash"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.modelOr(""); got != "gemini-2.5-flash" {
		t.Fatalf("modelOr empty = %q", got)
	}
	if got := c.modelOr("gemini-2.5-pro"); got != "gemini-2.5-pro" {
		t.Fatalf("modelOr explicit = %q", got)
	}
}
