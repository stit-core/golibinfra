package flagkit

import (
	"testing"
)

func TestEnvStore_Bool(t *testing.T) {
	store := NewEnvStore(Config{Definitions: "foo=true,bar=baz,boolfalse=false,booltrue=1"})

	tests := []struct {
		name string
		want bool
	}{
		{"foo", true},
		{"bar", false},
		{"boolfalse", false},
		{"booltrue", true},
		{"unknown", false},
	}

	for _, tt := range tests {
		got := store.Bool(tt.name)
		if got != tt.want {
			t.Errorf("Bool(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestEnvStore_String(t *testing.T) {
	store := NewEnvStore(Config{Definitions: "foo=true,bar=baz"})

	tests := []struct {
		name string
		want string
	}{
		{"foo", "true"},
		{"bar", "baz"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		got := store.String(tt.name)
		if got != tt.want {
			t.Errorf("String(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestEnvStore_Empty(t *testing.T) {
	store := NewEnvStore(Config{})

	if store.Bool("anything") {
		t.Error("expected false for empty store")
	}
	if store.String("anything") != "" {
		t.Error("expected empty string for empty store")
	}
}
