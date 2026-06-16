package mapkit_test

import (
	"reflect"
	"testing"

	"git.stit.tech/stit-core/golibinfra/mapkit"
)

func TestString(t *testing.T) {
	m := map[string]any{"s": "v", "n": 1.0}
	if got := mapkit.String(m, "s"); got != "v" {
		t.Fatalf("String(s) = %q, want v", got)
	}
	if got := mapkit.String(m, "n"); got != "" {
		t.Fatalf("String(n) = %q, want \"\" (wrong type)", got)
	}
	if got := mapkit.String(m, "absent"); got != "" {
		t.Fatalf("String(absent) = %q, want \"\"", got)
	}
}

func TestStrings(t *testing.T) {
	m := map[string]any{"xs": []any{"a", 2.0, "b"}, "bad": "nope"}
	if got := mapkit.Strings(m, "xs"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("Strings(xs) = %v, want [a b] (non-string dropped)", got)
	}
	if got := mapkit.Strings(m, "bad"); got != nil {
		t.Fatalf("Strings(bad) = %v, want nil", got)
	}
	if got := mapkit.Strings(m, "absent"); got != nil {
		t.Fatalf("Strings(absent) = %v, want nil", got)
	}
}

func TestFloatIntInt64(t *testing.T) {
	m := map[string]any{"f": 3.7, "i": 5, "i64": int64(9)}
	if got := mapkit.Float(m, "f"); got != 3.7 {
		t.Fatalf("Float(f) = %v, want 3.7", got)
	}
	if got := mapkit.Int(m, "f"); got != 3 {
		t.Fatalf("Int(f) = %d, want 3 (truncate)", got)
	}
	if got := mapkit.Int(m, "i"); got != 5 {
		t.Fatalf("Int(i) = %d, want 5 (int literal)", got)
	}
	if got := mapkit.Int64(m, "i64"); got != 9 {
		t.Fatalf("Int64(i64) = %d, want 9 (int64 literal)", got)
	}
	if got := mapkit.Float(m, "absent"); got != 0 {
		t.Fatalf("Float(absent) = %v, want 0", got)
	}
}

func TestMap(t *testing.T) {
	nested := map[string]any{"x": 1.0}
	m := map[string]any{"obj": nested, "bad": "nope"}
	if got := mapkit.Map(m, "obj"); !reflect.DeepEqual(got, nested) {
		t.Fatalf("Map(obj) = %v, want %v", got, nested)
	}
	if got := mapkit.Map(m, "bad"); got != nil {
		t.Fatalf("Map(bad) = %v, want nil", got)
	}
}

func TestIntPtr(t *testing.T) {
	m := map[string]any{"zero": 0.0, "n": 4.0}
	if p, ok := mapkit.IntPtr(m, "zero"); !ok || p == nil || *p != 0 {
		t.Fatalf("IntPtr(zero) = (%v, %v), want (*0, true) — key present distinguishes from absent", p, ok)
	}
	if p, ok := mapkit.IntPtr(m, "n"); !ok || *p != 4 {
		t.Fatalf("IntPtr(n) = (%v, %v), want (*4, true)", p, ok)
	}
	if p, ok := mapkit.IntPtr(m, "absent"); ok || p != nil {
		t.Fatalf("IntPtr(absent) = (%v, %v), want (nil, false)", p, ok)
	}
}

func TestBoolPtr(t *testing.T) {
	m := map[string]any{"b": true, "bad": "nope"}
	if p := mapkit.BoolPtr(m, "b"); p == nil || *p != true {
		t.Fatalf("BoolPtr(b) = %v, want *true", p)
	}
	if p := mapkit.BoolPtr(m, "bad"); p != nil {
		t.Fatalf("BoolPtr(bad) = %v, want nil (wrong type)", p)
	}
	if p := mapkit.BoolPtr(m, "absent"); p != nil {
		t.Fatalf("BoolPtr(absent) = %v, want nil", p)
	}
}
