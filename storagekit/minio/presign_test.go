package minio_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
	skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
)

// Presign SigV4 — локальная операция (Region задан → без bucket-location
// lookup), поэтому unit-тест не требует живого сервера.
func newPresigner(t *testing.T) *skminio.Storage {
	t.Helper()
	cfg := unitConfig()
	client, err := skminio.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	urls := storagekit.NewURLBuilder("https://cdn.example.com", cfg.Bucket, cfg.URLStyle)
	return skminio.NewStorage(client, cfg, urls, logkit.Nop())
}

func TestStorage_PresignGet(t *testing.T) {
	st := newPresigner(t)

	link, err := st.PresignGet(t.Context(), "reports/2026-07.pdf", 15*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	for _, want := range []string{"media", "reports/2026-07.pdf", "X-Amz-Signature", "X-Amz-Expires"} {
		if !strings.Contains(link, want) {
			t.Errorf("presigned url %q missing %q", link, want)
		}
	}
}

func TestStorage_PresignGetResponseHeaders(t *testing.T) {
	st := newPresigner(t)

	link, err := st.PresignGet(t.Context(), "reports/2026-07.pdf", time.Hour,
		storagekit.WithResponseContentDisposition(`attachment; filename="report.pdf"`),
		storagekit.WithResponseContentType("application/pdf"))
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse presigned url: %v", err)
	}
	q := u.Query()
	if got := q.Get("response-content-disposition"); got != `attachment; filename="report.pdf"` {
		t.Errorf("response-content-disposition = %q", got)
	}
	if got := q.Get("response-content-type"); got != "application/pdf" {
		t.Errorf("response-content-type = %q", got)
	}
	if !strings.Contains(q.Get("X-Amz-SignedHeaders"), "host") {
		t.Errorf("presigned url is not signed: %q", link)
	}
}

func TestStorage_PresignPut(t *testing.T) {
	st := newPresigner(t)

	link, err := st.PresignPut(t.Context(), "uploads/new.bin", time.Hour)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	for _, want := range []string{"media", "uploads/new.bin", "X-Amz-Signature"} {
		if !strings.Contains(link, want) {
			t.Errorf("presigned url %q missing %q", link, want)
		}
	}
}

func TestStorage_PresignValidatesArgs(t *testing.T) {
	st := newPresigner(t)

	if _, err := st.PresignGet(t.Context(), "", time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Errorf("PresignGet with empty key: err = %v, want ErrPresignFailed", err)
	}
	if _, err := st.PresignPut(t.Context(), "", time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Errorf("PresignPut with empty key: err = %v, want ErrPresignFailed", err)
	}
	for _, ttl := range []time.Duration{0, 500 * time.Millisecond, storagekit.MaxPresignTTL + time.Second} {
		if _, err := st.PresignGet(t.Context(), "k.bin", ttl); !errors.Is(err, storagekit.ErrPresignFailed) {
			t.Errorf("PresignGet(ttl=%s): err = %v, want ErrPresignFailed", ttl, err)
		}
	}
}
