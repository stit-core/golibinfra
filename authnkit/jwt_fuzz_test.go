package authnkit

import "testing"

func FuzzJWTParse(f *testing.F) {
	h := NewHS256JWT(JWTConfig{Secret: "fuzz-secret"})
	f.Add("")
	f.Add("not.a.token")
	f.Add("aaa.bbb.ccc")
	f.Add("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ4In0.sig")

	f.Fuzz(func(t *testing.T, in string) {
		_, _ = h.Parse(in)
	})
}
