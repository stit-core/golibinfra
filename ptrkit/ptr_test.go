package ptrkit_test

import (
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/ptrkit"
)

func TestOf(t *testing.T) {
	if got := ptrkit.Of(""); got == nil || *got != "" {
		t.Fatalf("Of(\"\") = %v, want pointer to empty string", got)
	}
	if got := ptrkit.Of(0); got == nil || *got != 0 {
		t.Fatalf("Of(0) = %v, want *0", got)
	}
	if got := ptrkit.Of([]int{1}); got == nil || len(*got) != 1 {
		t.Fatalf("Of(slice) = %v, want pointer (no comparable constraint)", got)
	}
	a, b := ptrkit.Of(1), ptrkit.Of(1)
	if a == b {
		t.Fatal("Of must return a pointer to a fresh copy, not a shared one")
	}
}

func TestOmitZero(t *testing.T) {
	t.Run("non-zero string returns pointer", func(t *testing.T) {
		got := ptrkit.OmitZero("x")
		if got == nil || *got != "x" {
			t.Fatalf("OmitZero(\"x\") = %v, want *\"x\"", got)
		}
	})
	t.Run("empty string returns nil", func(t *testing.T) {
		if got := ptrkit.OmitZero(""); got != nil {
			t.Fatalf("OmitZero(\"\") = %v, want nil", got)
		}
	})
	t.Run("non-zero int64 returns pointer", func(t *testing.T) {
		got := ptrkit.OmitZero(int64(7))
		if got == nil || *got != 7 {
			t.Fatalf("OmitZero(7) = %v, want *7", got)
		}
	})
	t.Run("zero int64 returns nil", func(t *testing.T) {
		if got := ptrkit.OmitZero(int64(0)); got != nil {
			t.Fatalf("OmitZero(0) = %v, want nil", got)
		}
	})
	t.Run("false returns nil", func(t *testing.T) {
		if got := ptrkit.OmitZero(false); got != nil {
			t.Fatalf("OmitZero(false) = %v, want nil", got)
		}
	})
	t.Run("comparable struct compares field-wise", func(t *testing.T) {
		type pair struct{ A, B int }
		if got := ptrkit.OmitZero(pair{}); got != nil {
			t.Fatalf("OmitZero(zero struct) = %v, want nil", got)
		}
		if got := ptrkit.OmitZero(pair{B: 1}); got == nil {
			t.Fatal("OmitZero(non-zero struct) = nil, want pointer")
		}
	})
	t.Run("copy is independent of the argument", func(t *testing.T) {
		v := "x"
		p := ptrkit.OmitZero(v)
		v += "y"
		if *p != "x" || v != "xy" {
			t.Fatalf("*p = %q (v = %q), want the value at call time", *p, v)
		}
	})
}

// time.Time сравнивается по ==, а не по IsZero: значение с location/монотонными
// часами не равно нулевому, поэтому OmitZero отдаёт указатель.
func TestOmitZero_TimeIsNotZeroAware(t *testing.T) {
	zoned := time.Time{}.In(time.FixedZone("x", 3600))
	if !zoned.IsZero() {
		t.Fatal("precondition: value must be IsZero")
	}
	if got := ptrkit.OmitZero(zoned); got == nil {
		t.Fatal("OmitZero(zero time in a zone) = nil, want pointer: == is not IsZero")
	}
	if got := ptrkit.OmitZero(time.Time{}); got != nil {
		t.Fatalf("OmitZero(time.Time{}) = %v, want nil", got)
	}
}

// any-значение сравнивается с nil-интерфейсом: динамические типы разные,
// поэтому даже несравнимое значение не паникует.
func TestOmitZero_AnyWithUncomparableValue(t *testing.T) {
	if got := ptrkit.OmitZero[any]([]int{1}); got == nil {
		t.Fatal("OmitZero[any](slice) = nil, want pointer")
	}
	if got := ptrkit.OmitZero[any](nil); got != nil {
		t.Fatalf("OmitZero[any](nil) = %v, want nil", got)
	}
}

func TestDeref(t *testing.T) {
	t.Run("nil string returns zero", func(t *testing.T) {
		if got := ptrkit.Deref[string](nil); got != "" {
			t.Fatalf("Deref(nil) = %q, want \"\"", got)
		}
	})
	t.Run("nil slice returns zero", func(t *testing.T) {
		if got := ptrkit.Deref[[]int](nil); got != nil {
			t.Fatalf("Deref[[]int](nil) = %v, want nil", got)
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
	if got := ptrkit.Deref(ptrkit.OmitZero("rt")); got != "rt" {
		t.Fatalf("Deref(OmitZero(\"rt\")) = %q, want \"rt\"", got)
	}
	if got := ptrkit.Deref(ptrkit.OmitZero("")); got != "" {
		t.Fatalf("Deref(OmitZero(\"\")) = %q, want \"\" (nil round-trips to zero)", got)
	}
	if got := ptrkit.Deref(ptrkit.Of("")); got != "" {
		t.Fatalf("Deref(Of(\"\")) = %q, want \"\"", got)
	}
}
