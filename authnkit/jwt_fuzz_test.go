package authnkit

import (
	"errors"
	"testing"
	"time"
)

func FuzzJWTParse(f *testing.F) {
	h, err := NewHS256JWT(JWTConfig{Secret: "fuzz-secret-key-of-at-least-32-bytes!!"})
	if err != nil {
		f.Fatalf("NewHS256JWT: %v", err)
	}
	valid, err := h.Sign(map[string]any{"sub": "x"}, time.Hour)
	if err != nil {
		f.Fatalf("Sign: %v", err)
	}
	f.Add("")
	f.Add("not.a.token")
	f.Add("aaa.bbb.ccc")
	f.Add("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ4In0.sig")
	f.Add("eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJ4IiwiZXhwIjo5OTk5OTk5OTk5fQ.")
	f.Add(valid)

	f.Fuzz(func(t *testing.T, in string) {
		_, err := h.Parse(in)
		if err != nil && !errors.Is(err, ErrTokenExpired) && !errors.Is(err, ErrTokenMalformed) && !errors.Is(err, ErrTokenSignature) {
			t.Fatalf("non-sentinel error: %v", err)
		}
	})
}
