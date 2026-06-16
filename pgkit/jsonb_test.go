package pgkit

import (
	"strings"
	"testing"
)

func TestUnmarshalJSONB(t *testing.T) {
	type payload struct {
		A int    `json:"a"`
		B string `json:"b"`
	}

	t.Run("empty input is no-op", func(t *testing.T) {
		dst := payload{A: 7}
		if err := UnmarshalJSONB(nil, &dst); err != nil {
			t.Fatalf("UnmarshalJSONB(nil) = %v, want nil", err)
		}
		if dst.A != 7 {
			t.Fatalf("dst mutated on empty input: %+v", dst)
		}
	})
	t.Run("valid jsonb decodes", func(t *testing.T) {
		var dst payload
		if err := UnmarshalJSONB([]byte(`{"a":3,"b":"x"}`), &dst); err != nil {
			t.Fatalf("UnmarshalJSONB = %v, want nil", err)
		}
		if dst.A != 3 || dst.B != "x" {
			t.Fatalf("decoded = %+v, want {3 x}", dst)
		}
	})
	t.Run("invalid jsonb wraps error with kit prefix", func(t *testing.T) {
		var dst payload
		err := UnmarshalJSONB([]byte(`{bad`), &dst)
		if err == nil {
			t.Fatal("UnmarshalJSONB(bad) = nil, want error")
		}
		if !strings.HasPrefix(err.Error(), "pgkit: unmarshal jsonb:") {
			t.Fatalf("error = %q, want kit prefix", err.Error())
		}
	})
}
