package session_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit/session"
)

func TestConfig_Validate(t *testing.T) {
	t.Parallel()
	ok := session.Config{IdleTTL: time.Hour, AbsoluteTTL: time.Hour, CleanupInterval: time.Minute}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate(%+v) = %v", ok, err)
	}
	for name, cfg := range map[string]session.Config{
		"zero":                {},
		"no idle":             {AbsoluteTTL: time.Hour, CleanupInterval: time.Minute},
		"absolute below idle": {IdleTTL: 2 * time.Hour, AbsoluteTTL: time.Hour, CleanupInterval: time.Minute},
		"no cleanup interval": {IdleTTL: time.Hour, AbsoluteTTL: time.Hour},
	} {
		if err := cfg.Validate(); !errors.Is(err, session.ErrConfig) {
			t.Errorf("%s: Validate = %v, want ErrConfig", name, err)
		}
	}
}

// Дефолты — часть контракта: сессия не живёт дольше 30 дней, токен — недели.
func TestNewConfig_Defaults(t *testing.T) {
	cfg, err := session.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.IdleTTL != 168*time.Hour || cfg.AbsoluteTTL != 720*time.Hour || cfg.CleanupInterval != time.Hour {
		t.Fatalf("defaults = %+v", cfg)
	}
	t.Setenv("SESSION_ABSOLUTE_TTL", "1h")
	if _, err := session.NewConfig(); !errors.Is(err, session.ErrConfig) {
		t.Fatalf("SESSION_ABSOLUTE_TTL below the idle default: err = %v, want ErrConfig", err)
	}
	t.Setenv("SESSION_ABSOLUTE_TTL", "soon")
	if _, err := session.NewConfig(); !errors.Is(err, session.ErrConfig) {
		t.Fatalf("unparsable SESSION_ABSOLUTE_TTL: err = %v, want ErrConfig", err)
	}
}

func TestCheckSubject(t *testing.T) {
	t.Parallel()
	if err := session.CheckSubject("user-1"); err != nil {
		t.Fatalf("CheckSubject(user-1) = %v", err)
	}
	if err := session.CheckSubject(strings.Repeat("x", session.MaxSubjectLen)); err != nil {
		t.Fatalf("subject of MaxSubjectLen bytes: %v", err)
	}
	for name, subject := range map[string]string{
		"empty":   "",
		"long":    strings.Repeat("x", session.MaxSubjectLen+1),
		"NUL":     "a\x00b",
		"invalid": "\xff",
	} {
		if err := session.CheckSubject(subject); !errors.Is(err, session.ErrInvalidSubject) {
			t.Errorf("%s: CheckSubject = %v, want ErrInvalidSubject", name, err)
		}
	}
}

func TestReusedError(t *testing.T) {
	t.Parallel()
	var err error = &session.ReusedError{SessionID: "s1", Subject: "u1"}
	if !errors.Is(err, session.ErrReused) || errors.Is(err, session.ErrInvalid) {
		t.Fatalf("errors.Is(ReusedError): ErrReused must match, ErrInvalid must not")
	}
	var reused *session.ReusedError
	if !errors.As(err, &reused) || reused.SessionID != "s1" || reused.Subject != "u1" {
		t.Fatalf("errors.As: %+v", reused)
	}
}
