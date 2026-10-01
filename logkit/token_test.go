package logkit_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Одноразовый токен authnkit (64 hex) дефолтные маски не пропускают ни в тексте
// сообщения, ни в значении под ключом, который секретом не считается.
func TestRedact_OneTimeTokenOfAuthnkit(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info", Format: "json"}, logkit.WithWriter(&buf))
	if err != nil {
		t.Fatalf("logkit.New: %v", err)
	}
	tok := authnkit.NewToken()
	log.Info(context.Background(), "reset link issued: "+tok, "url", "https://app.example/reset?t="+tok)
	if out := buf.String(); strings.Contains(out, tok) || !strings.Contains(out, "reset link issued") {
		t.Fatalf("token leaked or record lost: %s", out)
	}
}
