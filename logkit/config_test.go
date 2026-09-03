package logkit

import (
	"reflect"
	"testing"

	"github.com/kelseyhightower/envconfig"
)

func TestRedactConfig_FromEnv(t *testing.T) {
	t.Setenv("LOG_REDACT_ENABLED", "true")
	t.Setenv("LOG_REDACT_PATTERNS", `\d{10,15}; secret-\w+ ;`)
	t.Setenv("LOG_REDACT_REPLACE_WITH", "***")

	cfg, err := NewRedactConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.ReplaceWith != "***" {
		t.Fatalf("cfg = %+v", cfg)
	}
	want := Patterns{`\d{10,15}`, `secret-\w+`}
	if !reflect.DeepEqual(cfg.Patterns, want) {
		t.Fatalf("Patterns = %q, want %q (split on ';', comma stays inside the quantifier)", cfg.Patterns, want)
	}
}

func TestRedactConfig_Defaults(t *testing.T) {
	var cfg RedactConfig
	if err := envconfig.Process("", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.ReplaceWith != "[REDACTED]" || len(cfg.Patterns) != 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestNewConfig_RejectsInvalidLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "verbose")
	if _, err := NewConfig(); err == nil {
		t.Fatal("NewConfig must fail on invalid LOG_LEVEL")
	}
}
