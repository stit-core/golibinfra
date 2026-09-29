package logkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// withFxLogger — граф с *slog.Logger цепочки logkit, пишущим в buf, и FxLogger.
func withFxLogger(t *testing.T, buf *bytes.Buffer, level string) fx.Option {
	t.Helper()
	sl, err := logkit.NewSlog(logkit.Config{Level: level}, logkit.WithWriter(buf))
	if err != nil {
		t.Fatalf("NewSlog: %v", err)
	}
	return fx.Options(fx.Supply(sl), logkit.FxLogger)
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
			var buf bytes.Buffer
			app := fx.New(withFxLogger(t, &buf, "info"), opt)
			err := app.Err()
			if err == nil {
				err = app.Start(t.Context())
			}
			if err == nil {
				t.Fatal("start must fail")
			}

			var found bool
			for _, m := range jsonLines(t, &buf) {
				if m["level"] == "ERROR" && strings.Contains(m["error"].(string), "boom: db unreachable") {
					found = true
				}
			}
			if !found {
				t.Fatalf("no ERROR line with the cause:\n%s", buf.String())
			}
		})
	}
}

// Рутина fx (provided, invoking, хуки) — DEBUG: при LOG_LEVEL=info лог чист.
func TestFxLogger_RoutineIsDebug(t *testing.T) {
	run := func(level string) *bytes.Buffer {
		var buf bytes.Buffer
		app := fx.New(
			withFxLogger(t, &buf, level),
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
		return &buf
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

// FxLogger берёт *slog.Logger из logkit.Module — граф собирается без
// дополнительных провайдеров.
func TestFxLogger_WithModule(t *testing.T) {
	app := fx.New(
		fx.Supply(logkit.Config{Silent: true}),
		logkit.Module,
		logkit.FxLogger,
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx.New: %v", err)
	}
}
