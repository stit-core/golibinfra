package issuer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/hubkit/issuer"
)

func refreshManager() *issuer.RefreshManager {
	return issuer.NewRefreshManager(
		issuer.NewMemoryRefreshStore(),
		issuer.RefreshConfig{TTL: time.Hour, TombstoneTTL: time.Hour},
	)
}

func sampleSession() issuer.Session {
	return issuer.Session{SID: "sess-1", EmployeeID: "emp-1", Subject: "sub-1", Username: "n.sycheva", Role: "employee"}
}

func TestRefresh_RotateInvalidatesOld(t *testing.T) {
	m := refreshManager()
	ctx := context.Background()

	tok, err := m.Issue(ctx, sampleSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	sess, newTok, err := m.Rotate(ctx, tok)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if sess.SID != "sess-1" || sess.EmployeeID != "emp-1" {
		t.Fatalf("session не восстановлена: %+v", sess)
	}
	if newTok == tok {
		t.Fatal("rotating refresh: новый токен совпал со старым")
	}

	// Старый токен после ротации одноразово погашен.
	if _, _, err := m.Rotate(ctx, tok); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("повторная ротация старого токена: ожидали ErrRefreshInvalid, получили %v", err)
	}
	// Новый токен рабочий (цепочка продолжается).
	if _, _, err := m.Rotate(ctx, newTok); err != nil {
		t.Fatalf("ротация нового токена: %v", err)
	}
}

func TestRefresh_UnknownToken(t *testing.T) {
	m := refreshManager()
	if _, _, err := m.Rotate(context.Background(), "never-issued"); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("ожидали ErrRefreshInvalid, получили %v", err)
	}
}

func TestRefresh_TombstoneOnLogout(t *testing.T) {
	m := refreshManager()
	ctx := context.Background()

	tok, err := m.Issue(ctx, sampleSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Логаут ставит tombstone по sid.
	if err := m.Revoke(ctx, "sess-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// Живой refresh-токен этой сессии больше не обменивается.
	if _, _, err := m.Rotate(ctx, tok); !errors.Is(err, issuer.ErrRefreshRevoked) {
		t.Fatalf("ожидали ErrRefreshRevoked, получили %v", err)
	}
}

func TestRefresh_MemoryStore_TTL(t *testing.T) {
	m := issuer.NewRefreshManager(
		issuer.NewMemoryRefreshStore(),
		issuer.RefreshConfig{TTL: -time.Second, TombstoneTTL: time.Hour},
	)
	ctx := context.Background()
	tok, err := m.Issue(ctx, sampleSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, _, err := m.Rotate(ctx, tok); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("протухший refresh: ожидали ErrRefreshInvalid, получили %v", err)
	}
}
