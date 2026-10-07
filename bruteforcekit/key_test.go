package bruteforcekit_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
)

func testConfig(ipv6Prefix int) bruteforcekit.Config {
	return bruteforcekit.Config{
		MaxFailures:         3,
		LockDuration:        time.Minute,
		SubjectMaxFailures:  20,
		SubjectLockDuration: time.Minute,
		CleanupRetention:    time.Hour,
		CleanupInterval:     time.Minute,
		IPv6Prefix:          ipv6Prefix,
	}
}

func TestConfig_NormalizeKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		ipv6Prefix  int
		subject     string
		ip          string
		wantSubject string
		wantIP      string
	}{
		{name: "ipv4", ipv6Prefix: 128, subject: "u@example.com", ip: "203.0.113.7", wantIP: "203.0.113.7"},
		{name: "subject case and spaces", ipv6Prefix: 128, subject: " User@Example.COM\t", ip: "203.0.113.7", wantSubject: "user@example.com", wantIP: "203.0.113.7"},
		{name: "subject unicode case", ipv6Prefix: 128, subject: "Пользователь", ip: "203.0.113.7", wantSubject: "пользователь", wantIP: "203.0.113.7"},
		{name: "ipv4 mapped", ipv6Prefix: 128, subject: "u", ip: "::ffff:203.0.113.7", wantIP: "203.0.113.7"},
		{name: "ipv6 expanded", ipv6Prefix: 128, subject: "u", ip: "0:0:0:0:0:0:0:1", wantIP: "::1"},
		{name: "ipv6 zone", ipv6Prefix: 128, subject: "u", ip: "fe80::1%eth0", wantIP: "fe80::1"},
		{name: "ipv6 uppercase", ipv6Prefix: 128, subject: "u", ip: "2001:DB8::1", wantIP: "2001:db8::1"},
		{name: "ipv6 /64", ipv6Prefix: 64, subject: "u", ip: "2001:db8:0:1:dead:beef:1:2", wantIP: "2001:db8:0:1::"},
		{name: "ipv4 with /64 prefix", ipv6Prefix: 64, subject: "u", ip: "203.0.113.7", wantIP: "203.0.113.7"},
		{name: "subject at limit", ipv6Prefix: 128, subject: strings.Repeat("s", bruteforcekit.MaxSubjectLen), ip: "203.0.113.7", wantIP: "203.0.113.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			subject, ip, err := testConfig(tt.ipv6Prefix).NormalizeKey(tt.subject, tt.ip)
			if err != nil {
				t.Fatalf("NormalizeKey: %v", err)
			}
			want := tt.wantSubject
			if want == "" {
				want = tt.subject
			}
			if subject != want {
				t.Fatalf("subject = %q, want %q", subject, want)
			}
			if ip != tt.wantIP {
				t.Fatalf("ip = %q, want %q", ip, tt.wantIP)
			}
		})
	}
}

// Все адреса одной /64 попадают в один бакет — перебор подсети не обходит lock.
func TestConfig_NormalizeKey_IPv6SubnetSharesBucket(t *testing.T) {
	t.Parallel()
	cfg := testConfig(64)
	_, first, err := cfg.NormalizeKey("u", "2001:db8:0:1::1")
	if err != nil {
		t.Fatalf("NormalizeKey: %v", err)
	}
	_, second, err := cfg.NormalizeKey("u", "2001:db8:0:1:ffff:ffff:ffff:ffff")
	if err != nil {
		t.Fatalf("NormalizeKey: %v", err)
	}
	if first != second {
		t.Fatalf("addresses of one /64 map to %q and %q", first, second)
	}

	_, other, err := cfg.NormalizeKey("u", "2001:db8:0:2::1")
	if err != nil {
		t.Fatalf("NormalizeKey: %v", err)
	}
	if other == first {
		t.Fatalf("different /64 subnets share bucket %q", other)
	}
}

func TestConfig_NormalizeKey_Invalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		subject string
		ip      string
	}{
		{name: "empty subject", subject: "", ip: "203.0.113.7"},
		{name: "blank subject", subject: " \t ", ip: "203.0.113.7"},
		{name: "NUL in subject", subject: "u\x00@example.com", ip: "203.0.113.7"},
		{name: "subject not UTF-8", subject: "u\xff@example.com", ip: "203.0.113.7"},
		{name: "empty ip", subject: "u", ip: ""},
		{name: "not an ip", subject: "u", ip: "example.com"},
		{name: "ip with port", subject: "u", ip: "203.0.113.7:443"},
		{name: "cidr", subject: "u", ip: "203.0.113.0/24"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := testConfig(128).NormalizeKey(tt.subject, tt.ip)
			if !errors.Is(err, bruteforcekit.ErrInvalidKey) {
				t.Fatalf("NormalizeKey(%q, %q) = %v, want ErrInvalidKey", tt.subject, tt.ip, err)
			}
			if !strings.HasPrefix(err.Error(), "bruteforcekit: ") {
				t.Fatalf("error = %q, want bruteforcekit: prefix", err)
			}
		})
	}
}

// Subject длиннее MaxSubjectLen учитывается по SHA-256: ключ фиксированной длины, один на
// разные регистры, свой у другого subject'а и не совпадает с настоящим логином.
func TestConfig_NormalizeKey_LongSubjectIsHashed(t *testing.T) {
	t.Parallel()
	cfg := testConfig(128)
	long := strings.Repeat("s", bruteforcekit.MaxSubjectLen+1)
	key, _, err := cfg.NormalizeKey(long, "203.0.113.7")
	if err != nil {
		t.Fatalf("NormalizeKey(long subject): %v", err)
	}
	if !strings.HasPrefix(key, "SHA256:") || len(key) != len("SHA256:")+64 {
		t.Fatalf("key = %q, want SHA256:<64 hex>", key)
	}
	upper, _, _ := cfg.NormalizeKey(" "+strings.ToUpper(long), "203.0.113.7")
	other, _, _ := cfg.NormalizeKey(long+"x", "203.0.113.7")
	if upper != key || other == key {
		t.Fatalf("keys: %q, case variant %q, other subject %q", key, upper, other)
	}
}
