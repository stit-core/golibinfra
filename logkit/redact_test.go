package logkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

func newRedacted(t *testing.T, buf *bytes.Buffer, cfg RedactConfig) Logger {
	t.Helper()
	cfg.Enabled = true
	return mustLogger(t, Config{Level: "debug"}, buf, WithRedact(cfg))
}

func TestRedact_AttrsAndMsg(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})

	l.Info(
		context.Background(), "login john.doe@example.com from +79161234567",
		"email", "john.doe@example.com",
		"phone", "+79161234567",
		"card", "4111 1111 1111 1111",
		"order_id", "ORD-42",
	)

	m := decodeLine(t, buf.Bytes())
	if m["msg"] != "login [REDACTED] from [REDACTED]" {
		t.Errorf("msg = %q", m["msg"])
	}
	if m["level"] != "INFO" {
		t.Errorf("level = %v (record must not be re-encoded)", m["level"])
	}
	for _, k := range []string{"email", "phone", "card"} {
		if m[k] != "[REDACTED]" {
			t.Errorf("%s = %v, want [REDACTED]", k, m[k])
		}
	}
	if m["order_id"] != "ORD-42" {
		t.Errorf("order_id = %v, must stay intact", m["order_id"])
	}
}

func TestRedact_NumbersStayIntact(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})

	l.Info(
		context.Background(), "timing",
		"elapsed", 1234567890123*time.Nanosecond,
		"unix_ms", int64(1725364800000),
		"ratio", 0.5,
		"ok", true,
	)

	m := decodeLine(t, buf.Bytes())
	if m["elapsed"] != float64(1234567890123) {
		t.Errorf("elapsed = %v (%T), want numeric 1234567890123", m["elapsed"], m["elapsed"])
	}
	if m["unix_ms"] != float64(1725364800000) {
		t.Errorf("unix_ms = %v, want numeric 1725364800000", m["unix_ms"])
	}
	if m["ratio"] != 0.5 || m["ok"] != true {
		t.Errorf("ratio/ok changed: %v / %v", m["ratio"], m["ok"])
	}
}

func TestRedact_CoversWithAttrsAndGroups(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})

	l.With("owner", "owner@example.com").Info(
		context.Background(), "grouped",
		"contact", map[string]any{"email": "nested@example.com", "age": 33},
	)

	m := decodeLine(t, buf.Bytes())
	if m["owner"] != "[REDACTED]" {
		t.Errorf("With() attr not redacted: %v", m["owner"])
	}
	contact, _ := m["contact"].(map[string]any)
	if contact["email"] != "[REDACTED]" {
		t.Errorf("nested email not redacted: %v", m["contact"])
	}
	if contact["age"] != float64(33) {
		t.Errorf("nested number changed: %v", contact["age"])
	}
}

func TestRedact_SlogGroupAndError(t *testing.T) {
	var buf bytes.Buffer
	sl, err := NewSlogWithWriter(Config{Level: "debug"}, &buf, WithRedact(RedactConfig{Enabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	sl.WithGroup("req").With("user", "u@example.com").Info(
		"done",
		"err", fmt.Errorf("mail to x@example.com failed"),
	)

	m := decodeLine(t, buf.Bytes())
	req, _ := m["req"].(map[string]any)
	if req["user"] != "[REDACTED]" {
		t.Errorf("group With attr not redacted: %v", m)
	}
	if req["err"] != "mail to [REDACTED] failed" {
		t.Errorf("error text not redacted: %v", req["err"])
	}
}

func TestRedact_CustomPatternsAndReplacement(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{Patterns: Patterns{`secret-\w+`}, ReplaceWith: "***"})

	l.Info(context.Background(), "token secret-abc", "email", "kept@example.com")

	m := decodeLine(t, buf.Bytes())
	if m["msg"] != "token ***" {
		t.Errorf("msg = %v", m["msg"])
	}
	if m["email"] != "kept@example.com" {
		t.Errorf("default patterns must not apply when custom ones are set: %v", m["email"])
	}
}

func TestRedact_Disabled(t *testing.T) {
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf, WithRedact(RedactConfig{Enabled: false}))
	l.Info(context.Background(), "user login", "email", "john.doe@example.com")

	if m := decodeLine(t, buf.Bytes()); m["email"] != "john.doe@example.com" {
		t.Errorf("email must not be redacted when disabled: %v", m["email"])
	}
}

func TestRedact_InvalidPatternIsError(t *testing.T) {
	_, err := NewWithWriter(Config{}, io.Discard, WithRedact(RedactConfig{Enabled: true, Patterns: Patterns{`(`}}))
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestRedact_PhonePatternIgnoresPlainNumbers(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})

	l.Info(context.Background(), "ids", "ten_digits", "1234567890", "ru_phone", "89161234567", "e164", "+441234567890")

	m := decodeLine(t, buf.Bytes())
	if m["ten_digits"] != "1234567890" {
		t.Errorf("plain 10-digit id must stay: %v", m["ten_digits"])
	}
	if m["ru_phone"] != "[REDACTED]" || m["e164"] != "[REDACTED]" {
		t.Errorf("phones must be redacted: %v / %v", m["ru_phone"], m["e164"])
	}
}
