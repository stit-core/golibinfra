package mapkit_test

import (
	"bytes"
	"encoding/json"
	"math"
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
	m := map[string]any{
		"xs":       []any{"a", 2.0, "b"},
		"literal":  []string{"a", "b"},
		"empty":    []any{},
		"nostring": []any{1.0, true},
		"ints":     []int{1, 2},
		"bad":      "nope",
	}

	if got := mapkit.Strings(m, "xs"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("Strings(xs) = %v, want [a b] (non-string dropped)", got)
	}
	if got := mapkit.Strings(m, "literal"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("Strings(literal) = %v, want [a b]", got)
	}
	for _, k := range []string{"empty", "nostring"} {
		got := mapkit.Strings(m, k)
		if got == nil || len(got) != 0 {
			t.Fatalf("Strings(%s) = %v, want empty non-nil slice", k, got)
		}
	}
	for _, k := range []string{"bad", "ints", "absent"} {
		if got := mapkit.Strings(m, k); got != nil {
			t.Fatalf("Strings(%s) = %v, want nil", k, got)
		}
	}
}

func TestStrings_LiteralIsCopied(t *testing.T) {
	src := []string{"a", "b"}
	m := map[string]any{"xs": src}

	got := mapkit.Strings(m, "xs")
	src[0] = "mutated"

	if got[0] != "a" {
		t.Fatalf("Strings returned a view of the source slice: got[0] = %q", got[0])
	}
}

func TestFloatIntInt64(t *testing.T) {
	m := map[string]any{"f": 3.7, "neg": -2.7, "i": 5, "i64": int64(9)}
	if got := mapkit.Float(m, "f"); got != 3.7 {
		t.Fatalf("Float(f) = %v, want 3.7", got)
	}
	if got := mapkit.Int(m, "f"); got != 3 {
		t.Fatalf("Int(f) = %d, want 3 (truncate)", got)
	}
	if got := mapkit.Int(m, "neg"); got != -2 {
		t.Fatalf("Int(neg) = %d, want -2 (truncate toward zero)", got)
	}
	if got := mapkit.Int64(m, "f"); got != 3 {
		t.Fatalf("Int64(f) = %d, want 3 (truncate)", got)
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

func TestNumericKinds(t *testing.T) {
	m := map[string]any{
		"int8":    int8(8),
		"int16":   int16(16),
		"int32":   int32(32),
		"uint":    uint(1),
		"uint8":   uint8(8),
		"uint16":  uint16(16),
		"uint32":  uint32(32),
		"uint64":  uint64(64),
		"float32": float32(2.5),
	}

	wantInt := map[string]int{
		"int8": 8, "int16": 16, "int32": 32,
		"uint": 1, "uint8": 8, "uint16": 16, "uint32": 32, "uint64": 64,
		"float32": 2,
	}
	for k, want := range wantInt {
		if got := mapkit.Int(m, k); got != want {
			t.Errorf("Int(%s) = %d, want %d", k, got, want)
		}
		if got := mapkit.Int64(m, k); got != int64(want) {
			t.Errorf("Int64(%s) = %d, want %d", k, got, want)
		}
		if p := mapkit.IntPtr(m, k); p == nil || *p != want {
			t.Errorf("IntPtr(%s) = %v, want *%d", k, p, want)
		}
	}
	wantFloat := map[string]float64{
		"int8": 8, "int16": 16, "int32": 32,
		"uint": 1, "uint8": 8, "uint16": 16, "uint32": 32, "uint64": 64,
		"float32": 2.5,
	}
	for k, want := range wantFloat {
		if got := mapkit.Float(m, k); got != want {
			t.Errorf("Float(%s) = %v, want %v", k, got, want)
		}
	}

	m64 := map[string]any{"int": 7, "int64": int64(8), "float64": 1.5, "num": json.Number("2.5")}
	for k, want := range map[string]float64{"int": 7, "int64": 8, "float64": 1.5, "num": 2.5} {
		if got := mapkit.Float(m64, k); got != want {
			t.Errorf("Float(%s) = %v, want %v", k, got, want)
		}
	}
	if got := mapkit.Float(m64, "absent"); got != 0 {
		t.Errorf("Float(absent) = %v, want 0", got)
	}
}

func TestOutOfRangeAndNonFiniteAreTypeMismatch(t *testing.T) {
	m := map[string]any{
		"huge":     1e300,
		"tiny":     -1e300,
		"overflow": 1e19,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
		"neginf":   math.Inf(-1),
		"uint64":   uint64(math.MaxInt64) + 1,
	}

	for k := range m {
		if got := mapkit.Int(m, k); got != 0 {
			t.Errorf("Int(%s) = %d, want 0 (not representable)", k, got)
		}
		if got := mapkit.Int64(m, k); got != 0 {
			t.Errorf("Int64(%s) = %d, want 0 (not representable)", k, got)
		}
		if p := mapkit.IntPtr(m, k); p != nil {
			t.Errorf("IntPtr(%s) = *%d, want nil (not representable)", k, *p)
		}
	}

	// Границы int64 остаются валидными.
	edges := map[string]any{
		"min": float64(math.MinInt64),
		"max": int64(math.MaxInt64),
	}
	if got := mapkit.Int64(edges, "min"); got != math.MinInt64 {
		t.Errorf("Int64(min) = %d, want MinInt64", got)
	}
	if got := mapkit.Int64(edges, "max"); got != math.MaxInt64 {
		t.Errorf("Int64(max) = %d, want MaxInt64", got)
	}
}

func TestUnmarshalledPayloadOutOfRange(t *testing.T) {
	var p map[string]any
	if err := json.Unmarshal([]byte(`{"huge": 1e300, "id": 42}`), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := mapkit.Int64(p, "huge"); got != 0 {
		t.Fatalf("Int64(huge) = %d, want 0", got)
	}
	if got := mapkit.Int64(p, "id"); got != 42 {
		t.Fatalf("Int64(id) = %d, want 42", got)
	}
}

func TestJSONNumber(t *testing.T) {
	dec := json.NewDecoder(bytes.NewBufferString(
		`{"big": 9007199254740993, "frac": 2.5, "exp": 1e3, "huge": 1e300}`))
	dec.UseNumber()

	var p map[string]any
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	p["bad"] = json.Number("abc")

	if got := mapkit.Int64(p, "big"); got != 9007199254740993 {
		t.Errorf("Int64(big) = %d, want 9007199254740993 (json.Number keeps precision)", got)
	}
	if got := mapkit.Int(p, "frac"); got != 2 {
		t.Errorf("Int(frac) = %d, want 2 (truncate)", got)
	}
	if got := mapkit.Float(p, "frac"); got != 2.5 {
		t.Errorf("Float(frac) = %v, want 2.5", got)
	}
	if got := mapkit.Int64(p, "exp"); got != 1000 {
		t.Errorf("Int64(exp) = %d, want 1000 (float fallback)", got)
	}
	if got := mapkit.Int64(p, "huge"); got != 0 {
		t.Errorf("Int64(huge) = %d, want 0 (out of range)", got)
	}
	if got := mapkit.Float(p, "bad"); got != 0 {
		t.Errorf("Float(bad) = %v, want 0 (unparsable number)", got)
	}
	if p := mapkit.IntPtr(p, "bad"); p != nil {
		t.Errorf("IntPtr(bad) = *%d, want nil", *p)
	}
}

// Без UseNumber целое > 2^53 теряет точность ещё в json.Unmarshal — ограничение
// float64, не декодера mapkit.
func TestPrecisionLossWithoutUseNumber(t *testing.T) {
	var p map[string]any
	if err := json.Unmarshal([]byte(`{"big": 9007199254740993}`), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := mapkit.Int64(p, "big"); got != 9007199254740992 {
		t.Fatalf("Int64(big) = %d, want 9007199254740992 (float64 rounding)", got)
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
	m := map[string]any{"zero": 0.0, "n": 4.0, "frac": 2.999, "null": nil, "str": "7"}
	if p := mapkit.IntPtr(m, "zero"); p == nil || *p != 0 {
		t.Fatalf("IntPtr(zero) = %v, want *0 — present zero differs from absent", p)
	}
	if p := mapkit.IntPtr(m, "n"); p == nil || *p != 4 {
		t.Fatalf("IntPtr(n) = %v, want *4", p)
	}
	if p := mapkit.IntPtr(m, "frac"); p == nil || *p != 2 {
		t.Fatalf("IntPtr(frac) = %v, want *2 (truncate)", p)
	}
	for _, k := range []string{"absent", "null", "str"} {
		if p := mapkit.IntPtr(m, k); p != nil {
			t.Fatalf("IntPtr(%s) = %v, want nil (absent/null/wrong type)", k, *p)
		}
	}
}

func TestBoolPtr(t *testing.T) {
	m := map[string]any{"b": true, "bad": "nope", "null": nil}
	if p := mapkit.BoolPtr(m, "b"); p == nil || !*p {
		t.Fatalf("BoolPtr(b) = %v, want *true", p)
	}
	if p := mapkit.BoolPtr(m, "bad"); p != nil {
		t.Fatalf("BoolPtr(bad) = %v, want nil (wrong type)", p)
	}
	if p := mapkit.BoolPtr(m, "absent"); p != nil {
		t.Fatalf("BoolPtr(absent) = %v, want nil", p)
	}
	if p := mapkit.BoolPtr(m, "null"); p != nil {
		t.Fatalf("BoolPtr(null) = %v, want nil", p)
	}
}

func TestNilMap(t *testing.T) {
	var m map[string]any

	if got := mapkit.String(m, "k"); got != "" {
		t.Errorf("String(nil map) = %q, want \"\"", got)
	}
	if got := mapkit.Strings(m, "k"); got != nil {
		t.Errorf("Strings(nil map) = %v, want nil", got)
	}
	if got := mapkit.Float(m, "k"); got != 0 {
		t.Errorf("Float(nil map) = %v, want 0", got)
	}
	if got := mapkit.Int(m, "k"); got != 0 {
		t.Errorf("Int(nil map) = %d, want 0", got)
	}
	if got := mapkit.Int64(m, "k"); got != 0 {
		t.Errorf("Int64(nil map) = %d, want 0", got)
	}
	if got := mapkit.Map(m, "k"); got != nil {
		t.Errorf("Map(nil map) = %v, want nil", got)
	}
	if got := mapkit.IntPtr(m, "k"); got != nil {
		t.Errorf("IntPtr(nil map) = %v, want nil", got)
	}
	if got := mapkit.BoolPtr(m, "k"); got != nil {
		t.Errorf("BoolPtr(nil map) = %v, want nil", got)
	}
}
