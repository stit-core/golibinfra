package sberaikit

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNewRequiresAuthKey(t *testing.T) {
	if _, err := New(Config{}, nil, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewWithKey(t *testing.T) {
	c, err := New(Config{AuthKey: "dGVzdA==", DefaultModel: "GigaChat-2"}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.DefaultModel != "GigaChat-2" {
		t.Fatalf("default model not set: %q", c.cfg.DefaultModel)
	}
	if c.hc == nil {
		t.Fatal("http client not built")
	}
}

// TestNewCertPoolEmbedded — встроенный Минцифры-bundle грузится и оба CA в пуле.
func TestNewCertPoolEmbedded(t *testing.T) {
	pool, err := newCertPool("")
	if err != nil {
		t.Fatalf("newCertPool(embedded): %v", err)
	}
	if pool == nil {
		t.Fatal("pool is nil")
	}
	assertRussianTrustedInPool(t, pool)
}

// TestNewCertPoolFromFile — bundle из Config.CABundlePath грузится (параметризация
// пути CA-bundle).
func TestNewCertPoolFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, russianTrustedCAPEM, 0o600); err != nil {
		t.Fatalf("write temp CA: %v", err)
	}
	pool, err := newCertPool(path)
	if err != nil {
		t.Fatalf("newCertPool(file): %v", err)
	}
	assertRussianTrustedInPool(t, pool)
}

func TestNewCertPoolBadPath(t *testing.T) {
	if _, err := newCertPool("/nonexistent/ca.pem"); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

// TestNewCertPoolRejectsGarbage — невалидный PEM не проходит (AppendCerts fail).
func TestNewCertPoolRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := newCertPool(path); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

// TestTLSConfigNeverInsecure — scoped pool, InsecureSkipVerify выключен by design.
func TestTLSConfigNeverInsecure(t *testing.T) {
	cfg, err := newTLSConfig("")
	if err != nil {
		t.Fatalf("newTLSConfig: %v", err)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must be false")
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs must be set (scoped pool)")
	}
	if cfg.MinVersion < 0x0303 { // TLS 1.2
		t.Fatalf("MinVersion = %x, want >= TLS1.2", cfg.MinVersion)
	}
}

// assertRussianTrustedInPool проверяет, что Минцифры-CA попали в пул: каждый
// сертификат bundle присутствует среди subjects пула.
func assertRussianTrustedInPool(t *testing.T, pool *x509.CertPool) {
	t.Helper()
	subjects := pool.Subjects() //nolint:staticcheck // Subjects достаточно для проверки наличия CA в тесте
	contains := func(raw []byte) bool {
		for _, s := range subjects {
			if string(s) == string(raw) {
				return true
			}
		}
		return false
	}

	var found int
	rest := russianTrustedCAPEM
	for len(rest) > 0 {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			t.Fatalf("parse bundle cert: %v", err)
		}
		if !contains(c.RawSubject) {
			t.Fatalf("CA %q not found in pool", c.Subject.CommonName)
		}
		found++
	}
	if found < 2 {
		t.Fatalf("expected root+sub (2 certs) in bundle, found %d", found)
	}
}

func TestCostKnownModel(t *testing.T) {
	// 1M total tokens @ 65_000_000 micro-RUB/MTok = 65_000_000 micro-RUB = 65 ₽.
	got, known := Cost("GigaChat-2", Usage{TotalTokens: 1_000_000})
	if !known {
		t.Fatal("expected known model")
	}
	if got != 65_000_000 {
		t.Fatalf("cost = %d, want 65000000", got)
	}
}

func TestCostEmbeddings(t *testing.T) {
	// 1M total @ 14_000_000/MTok = 14_000_000 micro-RUB.
	got, _ := Cost("Embeddings", Usage{TotalTokens: 1_000_000})
	if got != 14_000_000 {
		t.Fatalf("cost = %d, want 14000000", got)
	}
}

func TestCostUnknownModel(t *testing.T) {
	if _, known := Cost("nonexistent", Usage{TotalTokens: 100}); known {
		t.Fatal("expected unknown model")
	}
}

func TestRawUsageNormalize(t *testing.T) {
	u := rawUsage{
		PromptTokens:          56,
		CompletionTokens:      31,
		TotalTokens:           87,
		PrecachedPromptTokens: 3,
		SystemTokens:          4,
	}.normalize()
	if u.InputTokens != 56 || u.OutputTokens != 31 || u.CachedTokens != 3 || u.TotalTokens != 87 || u.SystemTokens != 4 {
		t.Fatalf("normalize = %+v", u)
	}
}

func TestRawUsageNormalizeTotalFallback(t *testing.T) {
	// total_tokens отсутствует → считаем prompt+completion.
	u := rawUsage{PromptTokens: 10, CompletionTokens: 5}.normalize()
	if u.TotalTokens != 15 {
		t.Fatalf("TotalTokens = %d, want 15", u.TotalTokens)
	}
}

func TestDecodeUsageRetainsExtra(t *testing.T) {
	u := decodeUsage([]byte(`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"mystery_tokens":7}`))
	if u.Extra["mystery_tokens"] != 7 {
		t.Fatalf("extra not retained: %+v", u.Extra)
	}
	if u.InputTokens != 10 || u.OutputTokens != 5 {
		t.Fatalf("known usage lost: %+v", u)
	}
}

func TestClassifyStatuses(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{400, ErrInvalidRequest},
		{401, ErrAuth},
		{403, ErrAuth},
		{404, ErrNotFound},
		{422, ErrInvalidRequest},
		{429, ErrRateLimited},
		{500, ErrServer},
		{503, ErrServer},
	}
	for _, c := range cases {
		err := classify(&APIError{StatusCode: c.status})
		if !errors.Is(err, c.want) {
			t.Fatalf("status %d → %v, want %v", c.status, err, c.want)
		}
	}
}
