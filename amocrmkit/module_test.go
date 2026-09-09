package amocrmkit

import (
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Граф из README собирается: Module забирает обе Config из графа.
func TestModuleBuildsClient(t *testing.T) {
	t.Setenv("AMOCRM_RATE_RPS", "7")

	var got *Client
	app := fxtest.New(
		t,
		clockkit.Module,
		logkit.Module,
		Module,
		fx.Provide(logkit.NewConfig, httpclient.NewConfig, NewConfig),
		fx.Populate(&got),
	)
	app.RequireStart().RequireStop()
	if got == nil {
		t.Fatal("Client is not built")
	}
	if got.cfg.RateRPS != 7 {
		t.Errorf("RateRPS = %d, want 7", got.cfg.RateRPS)
	}
}

// Без Config кита граф не валиден — ошибка на старте, а не на первом запросе.
func TestModuleRequiresConfig(t *testing.T) {
	err := fx.ValidateApp(
		clockkit.Module,
		logkit.Module,
		Module,
		fx.Provide(logkit.NewConfig, httpclient.NewConfig),
		fx.Invoke(func(*Client) {}),
	)
	if err == nil {
		t.Fatal("a graph without amocrmkit.Config must be invalid")
	}
}

func TestNewConfigFromEnv(t *testing.T) {
	t.Setenv("AMOCRM_RATE_RPS", "3")
	t.Setenv("AMOCRM_MAX_WAIT", "250ms")
	t.Setenv("AMOCRM_HOST_OVERRIDE", "http://fake-amo:8080")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.RateRPS != 3 || cfg.MaxWait != 250*time.Millisecond || cfg.HostOverride != "http://fake-amo:8080" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestNewConfigValidatesEnv(t *testing.T) {
	t.Setenv("AMOCRM_RATE_RPS", "0")
	if _, err := NewConfig(); err == nil {
		t.Fatal("RateRPS=0 must be rejected at startup")
	}

	t.Setenv("AMOCRM_RATE_RPS", "not-a-number")
	if _, err := NewConfig(); err == nil {
		t.Fatal("a non-numeric RateRPS must be rejected")
	}
}

// Дефолты окружения: пустой ENV даёт рабочий конфиг.
func TestNewConfigDefaults(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.RateRPS != 7 || cfg.MaxWait != defaultMaxWait || cfg.HostOverride != "" {
		t.Fatalf("cfg = %+v", cfg)
	}
}
