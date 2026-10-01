package clientip

import (
	"context"
	"net/netip"
	"testing"
)

func TestWithFrom(t *testing.T) {
	if From(context.Background()).IsValid() {
		t.Fatal("empty ctx must give an invalid address")
	}
	ctx := With(context.Background(), netip.MustParseAddr("::ffff:203.0.113.7"))
	if got := From(ctx); got != netip.MustParseAddr("203.0.113.7") {
		t.Fatalf("From = %s, want the canonical 203.0.113.7", got)
	}
}

func TestKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"2001:db8:1:2::1":      "2001:db8:1:2::",
		"2001:DB8:1:2:ffff::9": "2001:db8:1:2::",
		"fe80::1%eth0":         "fe80::",
		"2001:db8:1:3::1":      "2001:db8:1:3::",
	} {
		if got := Key(netip.MustParseAddr(in), 64).String(); got != want {
			t.Errorf("Key(%s, 64) = %s, want %s", in, got, want)
		}
	}
	if got := Key(netip.MustParseAddr("2001:db8::1"), 128).String(); got != "2001:db8::1" {
		t.Errorf("Key(/128) = %s, want the address itself", got)
	}
	if Key(netip.Addr{}, 64).IsValid() {
		t.Error("an invalid address stays invalid")
	}
}
