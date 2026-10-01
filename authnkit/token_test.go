package authnkit_test

import (
	"regexp"
	"testing"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

func TestNewToken_HexOf256Bits(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 64 {
		tok := authnkit.NewToken()
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tok) {
			t.Fatalf("NewToken() = %q, want 64 lowercase hex chars (256 bits)", tok)
		}
		if seen[tok] {
			t.Fatalf("NewToken() repeated %q", tok)
		}
		seen[tok] = true
	}
}

func TestHashToken_SHA256Hex(t *testing.T) {
	t.Parallel()
	// sha256("abc") — вектор FIPS 180-2.
	if got, want := authnkit.HashToken("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
		t.Fatalf("HashToken(abc) = %s, want %s", got, want)
	}
	tok := authnkit.NewToken()
	first, second := authnkit.HashToken(tok), authnkit.HashToken(tok)
	if first != second || first == tok || len(first) != 64 {
		t.Fatalf("HashToken(%s) = %s, then %s: want one 64-hex digest, not the token", tok, first, second)
	}
}
