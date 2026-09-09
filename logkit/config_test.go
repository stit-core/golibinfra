package logkit

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

// clearEnv убирает переменные, заданные в окружении разработчика: envconfig не
// применяет default к явно пустому значению, поэтому t.Setenv тут не подходит.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { _ = os.Setenv(k, v) }) //nolint:usetesting // восстановление значения после теста: t.Setenv в Cleanup не работает
			_ = os.Unsetenv(k)
		}
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := map[string]struct {
		cfg Config
		ok  bool
	}{
		"zero value":      {Config{}, true},
		"level any case":  {Config{Level: "WARN"}, true},
		"format json":     {Config{Format: "json"}, true},
		"format text":     {Config{Format: "TEXT"}, true},
		"bad level":       {Config{Level: "verbose"}, false},
		"bad format":      {Config{Format: "logfmt"}, false},
		"source + silent": {Config{Source: true, Silent: true}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrConfig) {
				t.Fatalf("Validate() = %v, want ErrConfig", err)
			}
		})
	}
}

func TestNewConfig_FromEnv(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "text")
	t.Setenv("LOG_SOURCE", "true")
	t.Setenv("LOG_SILENT", "true")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Level: "debug", Format: "text", Source: true, Silent: true}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestNewConfig_Defaults(t *testing.T) {
	clearEnv(t, "LOG_LEVEL", "LOG_FORMAT", "LOG_SOURCE", "LOG_SILENT")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Level != "info" || cfg.Format != "json" || cfg.Source || cfg.Silent {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestNewConfig_RejectsInvalidLevelAndFormat(t *testing.T) {
	t.Run("level", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "verbose")
		if _, err := NewConfig(); !errors.Is(err, ErrConfig) {
			t.Fatalf("err = %v, want ErrConfig", err)
		}
	})
	t.Run("format", func(t *testing.T) {
		t.Setenv("LOG_FORMAT", "xml")
		if _, err := NewConfig(); !errors.Is(err, ErrConfig) {
			t.Fatalf("err = %v, want ErrConfig", err)
		}
	})
}

func TestNewRedactConfig_FromEnv(t *testing.T) {
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

func TestNewRedactConfig_Defaults(t *testing.T) {
	clearEnv(t, "LOG_REDACT_ENABLED", "LOG_REDACT_PATTERNS", "LOG_REDACT_REPLACE_WITH")

	cfg, err := NewRedactConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.ReplaceWith != "[REDACTED]" || len(cfg.Patterns) != 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestNewRedactConfig_Validates(t *testing.T) {
	t.Run("pattern", func(t *testing.T) {
		t.Setenv("LOG_REDACT_PATTERNS", `(`)
		if _, err := NewRedactConfig(); !errors.Is(err, ErrConfig) {
			t.Fatalf("err = %v, want ErrConfig", err)
		}
	})
	t.Run("replacement", func(t *testing.T) {
		t.Setenv("LOG_REDACT_REPLACE_WITH", `"`)
		if _, err := NewRedactConfig(); !errors.Is(err, ErrConfig) {
			t.Fatalf("err = %v, want ErrConfig", err)
		}
	})
}

func TestNewConfig_ReportsEnvconfigError(t *testing.T) {
	t.Setenv("LOG_SILENT", "maybe")
	if _, err := NewConfig(); err == nil {
		t.Fatal("NewConfig must fail on a non-boolean LOG_SILENT")
	}
	t.Setenv("LOG_REDACT_ENABLED", "maybe")
	if _, err := NewRedactConfig(); err == nil {
		t.Fatal("NewRedactConfig must fail on a non-boolean LOG_REDACT_ENABLED")
	}
}

func TestRedactConfig_Validate(t *testing.T) {
	cases := map[string]struct {
		cfg RedactConfig
		ok  bool
	}{
		"zero value":         {RedactConfig{}, true},
		"custom patterns":    {RedactConfig{Patterns: Patterns{`secret-\w+`}}, true},
		"broken pattern":     {RedactConfig{Patterns: Patterns{`(`}}, false},
		"quote replacement":  {RedactConfig{ReplaceWith: `"x"`}, false},
		"escape replacement": {RedactConfig{ReplaceWith: `\`}, false},
		"control character":  {RedactConfig{ReplaceWith: "a\nb"}, false},
		"dollar replacement": {RedactConfig{ReplaceWith: "$1"}, true},
		// Регрессия: паттерн с пустым совпадением маскирует каждую позицию
		// записи — весь лог превращается в цепочку замен.
		"star quantifier":   {RedactConfig{Patterns: Patterns{`a*`}}, false},
		"optional":          {RedactConfig{Patterns: Patterns{`x?`}}, false},
		"empty pattern":     {RedactConfig{Patterns: Patterns{``}}, false},
		"empty alternative": {RedactConfig{Patterns: Patterns{`secret|`}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrConfig) {
				t.Fatalf("Validate() = %v, want ErrConfig", err)
			}
		})
	}
}

func TestPatterns_DecodeEscapedSeparator(t *testing.T) {
	var p Patterns
	if err := p.Decode(`a\;b; c\d{2}`); err != nil {
		t.Fatal(err)
	}
	want := Patterns{`a\;b`, `c\d{2}`}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("Patterns = %q, want %q", p, want)
	}
	if err := (RedactConfig{Patterns: p}).Validate(); err != nil {
		t.Fatalf("escaped separator must stay a valid regex: %v", err)
	}
}

func TestNewServiceConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		clearEnv(t, "OTEL_SERVICE_NAME", "APP_ENV", "OTEL_SERVICE_VERSION")

		cfg, err := NewServiceConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Service != "" || cfg.Env != "dev" || cfg.Version != "dev" {
			t.Fatalf("cfg = %+v", cfg)
		}
	})
	t.Run("from env", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "api")
		t.Setenv("APP_ENV", "prod")
		t.Setenv("OTEL_SERVICE_VERSION", "1.2.3")

		cfg, err := NewServiceConfig()
		if err != nil {
			t.Fatal(err)
		}
		if (cfg != ServiceConfig{Service: "api", Env: "prod", Version: "1.2.3"}) {
			t.Fatalf("cfg = %+v", cfg)
		}
	})
}
