package requestid

import (
	"context"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	ctx := With(context.Background(), "abc")
	if got := From(ctx); got != "abc" {
		t.Fatalf("got %q", got)
	}
	if got := From(context.Background()); got != "" {
		t.Fatalf("empty ctx: got %q", got)
	}
}

func TestValid(t *testing.T) {
	cases := map[string]bool{
		"":                             false,
		"req-1":                        true,
		"7f3c1e2a-0b4d-4f5e-9a6b-1c2d": true,
		"with space":                   false,
		"tab\there":                    false,
		"кириллица":                    false,
		strings.Repeat("x", MaxLen):    true,
		strings.Repeat("x", MaxLen+1):  false,
	}
	for id, want := range cases {
		if got := Valid(id); got != want {
			t.Errorf("Valid(%q) = %v, want %v", id, got, want)
		}
	}
}
