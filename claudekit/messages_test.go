package claudekit

import (
	"errors"
	"testing"
)

func TestNewRequiresAPIKey(t *testing.T) {
	if _, err := New(Config{}, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewWithKey(t *testing.T) {
	c, err := New(Config{APIKey: "sk-test", DefaultModel: "claude-opus-4-8"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.DefaultModel != "claude-opus-4-8" {
		t.Fatalf("default model not set: %q", c.cfg.DefaultModel)
	}
}

func TestResponseTextAndRefusal(t *testing.T) {
	r := &Response{
		Content:    []Block{TextBlock{Text: "a"}, ThinkingBlock{Thinking: "x"}, TextBlock{Text: "b"}},
		StopReason: StopRefusal,
	}
	if got := r.Text(); got != "ab" {
		t.Fatalf("Text() = %q, want %q", got, "ab")
	}
	if !r.IsRefusal() {
		t.Fatal("IsRefusal() = false, want true")
	}
}

func TestSystemText(t *testing.T) {
	s := SystemText("hi")
	if len(s) != 1 || s[0].Text != "hi" {
		t.Fatalf("SystemText = %+v", s)
	}
}

func TestBlockToParamUnknown(t *testing.T) {
	type bogus struct{ Block }
	if _, err := blockToParam(bogus{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}
