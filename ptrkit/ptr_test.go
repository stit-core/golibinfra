package ptrkit_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/ptrkit"
)

func TestPtr(t *testing.T) {
	t.Run("non-zero string returns pointer", func(t *testing.T) {
		got := ptrkit.Ptr("x")
		if got == nil || *got != "x" {
			t.Fatalf("Ptr(\"x\") = %v, want *\"x\"", got)
		}
	})
	t.Run("empty string returns nil", func(t *testing.T) {
		if got := ptrkit.Ptr(""); got != nil {
			t.Fatalf("Ptr(\"\") = %v, want nil", got)
		}
	})
	t.Run("non-zero int64 returns pointer", func(t *testing.T) {
		got := ptrkit.Ptr(int64(7))
		if got == nil || *got != 7 {
			t.Fatalf("Ptr(7) = %v, want *7", got)
		}
	})
	t.Run("zero int64 returns nil", func(t *testing.T) {
		if got := ptrkit.Ptr(int64(0)); got != nil {
			t.Fatalf("Ptr(0) = %v, want nil", got)
		}
	})
}

func TestDeref(t *testing.T) {
	t.Run("nil string returns zero", func(t *testing.T) {
		if got := ptrkit.Deref[string](nil); got != "" {
			t.Fatalf("Deref(nil) = %q, want \"\"", got)
		}
	})
	t.Run("non-nil returns value", func(t *testing.T) {
		v := int64(42)
		if got := ptrkit.Deref(&v); got != 42 {
			t.Fatalf("Deref(&42) = %d, want 42", got)
		}
	})
}

func TestRoundTrip(t *testing.T) {
	if got := ptrkit.Deref(ptrkit.Ptr("rt")); got != "rt" {
		t.Fatalf("Deref(Ptr(\"rt\")) = %q, want \"rt\"", got)
	}
	if got := ptrkit.Deref(ptrkit.Ptr("")); got != "" {
		t.Fatalf("Deref(Ptr(\"\")) = %q, want \"\" (nil round-trips to zero)", got)
	}
}
