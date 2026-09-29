package logkit_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/telemetry"
)

// Сниппет README § Быстрый старт → «Через fx» собирается как есть.
func TestReadme_QuickStartWithFx(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(telemetry.NewConfig),
		fx.Provide(logkit.NewConfig),
		fx.Provide(logkit.NewRedactConfig),
		telemetry.Module,
		logkit.Module,
		logkit.FxLogger,
		fx.Invoke(func(lc fx.Lifecycle, log logkit.Logger) {
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					log.Info(ctx, "service started")
					return nil
				},
			})
		}),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// Регрессия: без telemetry.Config OTel-sink молча не подключался — граф
// собирался, логи в OTLP не уходили. Теперь это ошибка сборки графа.
func TestReadme_QuickStartWithFxNeedsTelemetryConfig(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(logkit.NewConfig),
		telemetry.Module,
		logkit.Module,
		fx.Invoke(func(logkit.Logger) {}),
	)
	if err == nil {
		t.Fatal("a graph with telemetry.Module but without telemetry.Config must fail")
	}
}

// Сниппет README § Быстрый старт → «Без fx» компилируется и работает как есть.
func TestReadme_QuickStartWithoutFx(t *testing.T) {
	var w bytes.Buffer
	redactCfg := logkit.RedactConfig{}
	svcCfg := logkit.ServiceConfig{Service: "api", Env: "dev", Version: "v1.2.3"}

	ctx := context.Background()

	cfg, err := logkit.NewConfig() // или logkit.Config{Level: "debug"}
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	cfg.Silent = true // сниппет пишет в stdout; тесту он не нужен
	log, err := logkit.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.Info(ctx, "started")

	sl, err := logkit.NewSlog(cfg) // *slog.Logger той же цепочки
	if err != nil {
		t.Fatalf("NewSlog: %v", err)
	}
	sl.Info("started")

	withOpts, err := logkit.New(cfg,
		logkit.WithWriter(&w),        // writer вместо stdout
		logkit.WithRedact(redactCfg), // PII-маски
		logkit.WithService(svcCfg),   // service/env/version в каждой записи
	)
	if err != nil {
		t.Fatalf("New with options: %v", err)
	}
	withOpts.Info(ctx, "login john@example.com")
	if got := w.String(); !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, `"service":"api"`) {
		t.Fatalf("options not applied: %s", got)
	}

	fromSlog := logkit.FromSlog(sl) // одна цепочка для обеих форм
	fromSlog.Info(ctx, "ok")
	if logkit.FromSlog(nil) == nil {
		t.Fatal("FromSlog(nil) must give Nop()")
	}
}
