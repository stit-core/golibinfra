package logkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/log"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// withFxLogger — граф с logkit.Config уровня level и FxLogger: логгер событий FxLogger строит сам и
// пишет в stdout процесса — его тест читает captureStdout.
func withFxLogger(level string) fx.Option {
	return fx.Options(fx.Supply(logkit.Config{Level: level}), logkit.FxLogger)
}

// captureStdout подменяет os.Stdout до сборки графа: FxLogger, как в проде, пишет туда, и снаружи
// его writer не задать. Чтение — параллельно записи, чтобы pipe не встал на заполненном буфере.
func captureStdout(t *testing.T) func() *bytes.Buffer {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })
	done := make(chan *bytes.Buffer)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- &buf
	}()
	return func() *bytes.Buffer {
		os.Stdout = old
		_ = w.Close()
		return <-done
	}
}

func jsonLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not a JSON line: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// errorLineWith — есть JSON-строка ERROR, чей error содержит cause.
func errorLineWith(t *testing.T, buf *bytes.Buffer, cause string) bool {
	t.Helper()
	for _, m := range jsonLines(t, buf) {
		if e, _ := m["error"].(string); m["level"] == "ERROR" && strings.Contains(e, cause) {
			return true
		}
	}
	return false
}

// Регрессия FX-05: причина падения старта — JSON-строка ERROR в логе сервиса,
// а не текст в stderr мимо logkit.
func TestFxLogger_StartFailureIsErrorJSON(t *testing.T) {
	cases := map[string]fx.Option{
		"constructor": fx.Options(
			fx.Provide(func() (*bytes.Reader, error) { return nil, errors.New("boom: db unreachable") }),
			fx.Invoke(func(*bytes.Reader) {}),
		),
		"OnStart": fx.Invoke(func(lc fx.Lifecycle) {
			lc.Append(fx.Hook{OnStart: func(context.Context) error { return errors.New("boom: db unreachable") }})
		}),
		"panic": fx.Options(
			fx.RecoverFromPanics(),
			fx.Invoke(func() { panic("boom: db unreachable") }),
		),
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			stdout := captureStdout(t)
			app := fx.New(withFxLogger("info"), opt)
			err := app.Err()
			if err == nil {
				err = app.Start(t.Context())
			}
			if err == nil {
				t.Fatal("start must fail")
			}
			if buf := stdout(); !errorLineWith(t, buf, "boom: db unreachable") {
				t.Fatalf("no ERROR line with the cause:\n%s", buf.String())
			}
		})
	}
}

// Регрессия KFX-06: отказ провайдера, который тянет граф logkit (ошибка конфига telemetry отдаёт
// log.LoggerProvider с ошибкой), — JSON-строка ERROR: логгер событий от графа logkit не зависит, а
// не падает вместе с ним в текст fx в stderr.
func TestFxLogger_LoggerProviderFailureIsErrorJSON(t *testing.T) {
	stdout := captureStdout(t)
	app := fx.New(
		withFxLogger("info"),
		fx.Provide(func() (log.LoggerProvider, error) {
			return nil, errors.New("telemetry: invalid config: OTEL_SERVICE_NAME is required")
		}),
		logkit.Module,
		fx.Invoke(func(logkit.Logger) {}),
	)
	if app.Err() == nil {
		t.Fatal("fx.New must fail")
	}
	if buf := stdout(); !errorLineWith(t, buf, "OTEL_SERVICE_NAME is required") {
		t.Fatalf("no ERROR line with the provider failure:\n%s", buf.String())
	}
}

// Маски FxLogger — из RedactConfig графа, как у Module: свой набор и замена действуют и на события
// fx, без RedactConfig — дефолтный набор.
func TestFxLogger_RedactsLikeModule(t *testing.T) {
	cause := errors.New("boom: dsn postgres://app:hunter2@db/app, owner john@example.com")
	for name, c := range map[string]struct {
		redact     fx.Option
		hidden     string
		replacedBy string
	}{
		"default set":  {fx.Options(), "john@example.com", "[REDACTED]"},
		"own patterns": {fx.Supply(logkit.RedactConfig{Patterns: []string{`hunter\d`}, ReplaceWith: "***"}), "hunter2", "***"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout := captureStdout(t)
			app := fx.New(withFxLogger("info"), c.redact,
				fx.Provide(func() (*bytes.Reader, error) { return nil, cause }),
				fx.Invoke(func(*bytes.Reader) {}))
			if app.Err() == nil {
				t.Fatal("fx.New must fail")
			}
			out := stdout().String()
			if strings.Contains(out, c.hidden) || !strings.Contains(out, c.replacedBy) {
				t.Fatalf("FxLogger must mask %q with %q:\n%s", c.hidden, c.replacedBy, out)
			}
		})
	}
}

// Рутина fx (provided, invoking, хуки) — DEBUG: при LOG_LEVEL=info лог чист.
func TestFxLogger_RoutineIsDebug(t *testing.T) {
	run := func(level string) *bytes.Buffer {
		stdout := captureStdout(t)
		app := fx.New(
			withFxLogger(level),
			fx.Invoke(func(lc fx.Lifecycle) {
				lc.Append(fx.Hook{OnStart: func(context.Context) error { return nil }})
			}),
		)
		if err := app.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := app.Stop(t.Context()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		return stdout()
	}

	lines := jsonLines(t, run("debug"))
	msgs := make(map[string]bool, len(lines))
	for _, m := range lines {
		if m["level"] != "DEBUG" {
			t.Fatalf("routine fx event not at DEBUG: %v", m)
		}
		msgs[m["msg"].(string)] = true
	}
	for _, want := range []string{"provided", "invoking", "OnStart hook executed", "started"} {
		if !msgs[want] {
			t.Errorf("fx event %q missing: %v", want, msgs)
		}
	}

	if out := run("info"); out.Len() != 0 {
		t.Fatalf("LOG_LEVEL=info must hide routine fx events:\n%s", out.String())
	}
}

// FxLogger не требует logkit.Module: логгер событий — из Config графа, а без Config граф не
// собирается.
func TestFxLogger_NeedsOnlyConfig(t *testing.T) {
	if err := fx.ValidateApp(fx.Supply(logkit.Config{Silent: true}), logkit.FxLogger); err != nil {
		t.Fatalf("ValidateApp with Config only: %v", err)
	}
	if err := fx.ValidateApp(logkit.FxLogger); err == nil {
		t.Fatal("ValidateApp without logkit.Config = nil, want a missing type error")
	}
}
