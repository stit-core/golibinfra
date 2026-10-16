package logkit_test

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// logEnv — переменные обоих конфигов логгера: тест задаёт каждую, пустая читается как незаданная (дефолт).
var logEnv = []string{
	"LOG_LEVEL", "LOG_FORMAT", "LOG_SOURCE", "LOG_SILENT",
	"LOG_REDACT_DISABLED", "LOG_REDACT_PATTERNS", "LOG_REDACT_REPLACE_WITH",
}

func setLogEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, key := range logEnv {
		t.Setenv(key, env[key])
	}
}

// Логгер команды разбирает окружение как граф с Module, NewConfig и NewRedactConfig: при одном окружении записи
// совпадают — формат, уровень, маски и замена, source — и окружение в них видно.
func TestNewFromEnv_ParsesLikeModule(t *testing.T) {
	ctx := context.Background()
	// emit пишет одни записи обоими логгерами из одной строки кода: source у них общий.
	emit := func(l logkit.Logger) {
		for _, write := range []func(context.Context, string, ...any){l.Debug, l.Info, l.Error} {
			write(ctx, "login john@example.com", "password", "hunter2", "note", "code hunter7")
		}
	}
	timeRe := regexp.MustCompile(`"time":"[^"]*"|time=\S+`)
	for _, tc := range []struct {
		name        string
		env         map[string]string
		has, hasNot []string
	}{
		{
			name: "defaults", env: map[string]string{},
			has:    []string{`"level":"INFO"`, `"level":"ERROR"`, "[REDACTED]"},
			hasNot: []string{"DEBUG", "john@example.com", "hunter2", `"source"`},
		},
		{
			name: "text, debug", env: map[string]string{"LOG_LEVEL": "debug", "LOG_FORMAT": "text"},
			has:    []string{"level=DEBUG", "level=ERROR", "[REDACTED]"},
			hasNot: []string{`"level"`, "john@example.com", "hunter2"},
		},
		{
			name: "own masks, source, error level",
			env: map[string]string{
				"LOG_LEVEL": "error", "LOG_SOURCE": "true", "LOG_REDACT_PATTERNS": `hunter\d`, "LOG_REDACT_REPLACE_WITH": "***",
			},
			has:    []string{`"level":"ERROR"`, `"source"`, "code ***", "john@example.com"},
			hasNot: []string{"INFO", "hunter7", "hunter2"},
		},
		{
			name: "redact disabled", env: map[string]string{"LOG_REDACT_DISABLED": "true"},
			has:    []string{"john@example.com", "hunter2"},
			hasNot: []string{"[REDACTED]"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setLogEnv(t, tc.env)
			stdout := captureStdout(t)
			fromEnv, err := logkit.NewFromEnv()
			if err != nil {
				t.Fatalf("NewFromEnv: %v", err)
			}
			var module logkit.Logger
			app := fx.New(fx.NopLogger, fx.Provide(logkit.NewConfig, logkit.NewRedactConfig), logkit.Module, fx.Populate(&module))
			if err := app.Err(); err != nil {
				t.Fatalf("fx: %v", err)
			}
			emit(fromEnv)
			emit(module)
			lines := strings.Split(strings.TrimSpace(timeRe.ReplaceAllString(stdout().String(), "time")), "\n")
			half := len(lines) / 2
			got, want := strings.Join(lines[:half], "\n"), strings.Join(lines[half:], "\n")
			if len(lines)%2 != 0 || got != want {
				t.Fatalf("NewFromEnv writes\n%s\nthe Module graph writes\n%s", got, want)
			}
			for _, s := range tc.has {
				if !strings.Contains(got, s) {
					t.Errorf("records must contain %q:\n%s", s, got)
				}
			}
			for _, s := range tc.hasNot {
				if strings.Contains(got, s) {
					t.Errorf("records must not contain %q:\n%s", s, got)
				}
			}
		})
	}
}

// Негодное значение любой переменной — ErrConfig с её именем, как у конфигов графа; паттерн проверяется и у
// выключенного redact.
func TestNewFromEnv_RejectsInvalidEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		key  string
	}{
		{"level", map[string]string{"LOG_LEVEL": "loud"}, "LOG_LEVEL"},
		{"format", map[string]string{"LOG_FORMAT": "xml"}, "LOG_FORMAT"},
		{"source", map[string]string{"LOG_SOURCE": "maybe"}, "LOG_SOURCE"},
		{"silent", map[string]string{"LOG_SILENT": "maybe"}, "LOG_SILENT"},
		{"redact switch", map[string]string{"LOG_REDACT_DISABLED": "maybe"}, "LOG_REDACT_DISABLED"},
		{"pattern", map[string]string{"LOG_REDACT_PATTERNS": "("}, "LOG_REDACT_PATTERNS"},
		{"pattern of disabled redact", map[string]string{"LOG_REDACT_PATTERNS": "(", "LOG_REDACT_DISABLED": "true"}, "LOG_REDACT_PATTERNS"},
		{"replacement", map[string]string{"LOG_REDACT_REPLACE_WITH": `"`}, "LOG_REDACT_REPLACE_WITH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setLogEnv(t, tc.env)
			log, err := logkit.NewFromEnv()
			if !errors.Is(err, logkit.ErrConfig) || !strings.Contains(err.Error(), tc.key) || log != nil {
				t.Fatalf("NewFromEnv() = %v, %v; want ErrConfig naming %s", log, err, tc.key)
			}
		})
	}
}

// У логгера команды нет OTel-sink'а: при LOG_SILENT=true stdout пуст, а записи ERROR — в stderr под масками, и
// причина отказа команды не теряется.
func TestNewFromEnv_SilentKeepsErrorsInStderr(t *testing.T) {
	setLogEnv(t, map[string]string{"LOG_SILENT": "true"})
	stdout, stderr := captureStdout(t), captureStream(t, &os.Stderr)
	log, err := logkit.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	ctx := context.Background()
	log.Warn(ctx, "degraded")
	log.Error(ctx, "migrate up failed", "error", errors.New("db of john@example.com is unreachable"))

	out, errOut := stdout(), stderr()
	if out.Len() != 0 {
		t.Fatalf("LOG_SILENT=true must keep stdout empty:\n%s", out)
	}
	if !errorLineWith(t, errOut, "is unreachable") || strings.Contains(errOut.String(), "john@example.com") {
		t.Fatalf("stderr must carry the masked cause of the failure:\n%s", errOut)
	}
	for _, m := range jsonLines(t, errOut) {
		if m["level"] != "ERROR" {
			t.Fatalf("only ERROR records go to stderr under LOG_SILENT, got %v", m)
		}
	}
}
