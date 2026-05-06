package logkit

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRedactHandler_Email(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithRedact(Config{Level: "debug"}, RedactConfig{Enabled: true, ReplaceWith: "[REDACTED]"}, &buf)

	log.Info(context.Background(), "user login", "email", "john.doe@example.com")

	out := buf.String()
	if strings.Contains(out, "john.doe@example.com") {
		t.Errorf("expected email to be redacted, got: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in output, got: %s", out)
	}
}

func TestRedactHandler_Phone(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithRedact(Config{Level: "debug"}, RedactConfig{Enabled: true, ReplaceWith: "[REDACTED]"}, &buf)

	log.Info(context.Background(), "user contact", "phone", "+79161234567")

	out := buf.String()
	if strings.Contains(out, "+79161234567") {
		t.Errorf("expected phone to be redacted, got: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in output, got: %s", out)
	}
}

func TestRedactHandler_Disabled(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithRedact(Config{Level: "debug"}, RedactConfig{Enabled: false}, &buf)

	log.Info(context.Background(), "user login", "email", "john.doe@example.com")

	out := buf.String()
	if !strings.Contains(out, "john.doe@example.com") {
		t.Errorf("expected email NOT redacted when disabled, got: %s", out)
	}
}

func TestRedactHandler_CreditCard(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithRedact(Config{Level: "debug"}, RedactConfig{Enabled: true, ReplaceWith: "[REDACTED]"}, &buf)

	log.Info(context.Background(), "payment", "card", "4111 1111 1111 1111")

	out := buf.String()
	if strings.Contains(out, "4111") {
		t.Errorf("expected credit card to be redacted, got: %s", out)
	}
}
