package config

import "testing"

type fuzzTarget struct {
	Secret string `vault:"secret"`
	Token  string `vault:"token"`
	Other  string `vault:"other"`
}

func FuzzApplyVaultData(f *testing.F) {
	f.Add("hello", "world", "x")
	f.Add("", "", "")
	f.Add("a\x00b", "long-"+stringRepeat("x", 256), "юникод")

	f.Fuzz(func(t *testing.T, secret, token, other string) {
		data := map[string]any{
			"secret": secret,
			"token":  token,
			"other":  other,
		}
		var dst fuzzTarget
		_ = applyVaultData(data, &dst)
	})
}

func stringRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
