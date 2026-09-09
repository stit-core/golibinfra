package config_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/config"
)

type appConfig struct {
	Addr    string `envconfig:"ADDR" default:":8080"`
	Workers int    `envconfig:"WORKERS" default:"2"`
	Debug   bool   `envconfig:"DEBUG"`
	Region  string
}

type pgSection struct {
	DSN string `envconfig:"PG_DSN"`
}

type nestedConfig struct {
	Postgres pgSection
}

func TestEnv_FillsByTagsAndDefaults(t *testing.T) {
	t.Setenv("ADDR", ":9090")
	t.Setenv("DEBUG", "true")

	var cfg appConfig
	if err := config.Load(context.Background(), &cfg, config.Env()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":9090" || cfg.Workers != 2 || !cfg.Debug {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestEnvPrefixed_PrefixedNameWins(t *testing.T) {
	t.Setenv("CFGTEST_ADDR", ":7070")
	t.Setenv("ADDR", ":1")

	var cfg appConfig
	if err := config.Load(context.Background(), &cfg, config.EnvPrefixed("cfgtest")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":7070" {
		t.Fatalf("Addr = %q, want value from CFGTEST_ADDR", cfg.Addr)
	}
}

func TestEnvPrefixed_FallsBackToUnprefixed(t *testing.T) {
	t.Setenv("ADDR", ":unprefixed")

	var cfg appConfig
	if err := config.Load(context.Background(), &cfg, config.EnvPrefixed("cfgtest")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":unprefixed" {
		t.Fatalf("Addr = %q, want fallback to unprefixed ADDR", cfg.Addr)
	}
}

// Fallback работает только для полей с envconfig-тегом: у поля без тега
// альтернативного (непрефиксованного) имени нет.
func TestEnvPrefixed_UntaggedFieldHasNoFallback(t *testing.T) {
	t.Setenv("REGION", "eu")

	var cfg appConfig
	if err := config.Load(context.Background(), &cfg, config.EnvPrefixed("cfgtest")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Region != "" {
		t.Fatalf("Region = %q, want empty: untagged field reads only CFGTEST_REGION", cfg.Region)
	}
}

func TestEnv_NestedStructFieldNameBecomesPrefix(t *testing.T) {
	t.Setenv("PG_DSN", "via-tag")
	t.Setenv("POSTGRES_PG_DSN", "via-field-prefix")

	var cfg nestedConfig
	if err := config.Load(context.Background(), &cfg, config.Env()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Postgres.DSN != "via-field-prefix" {
		t.Fatalf("DSN = %q, want POSTGRES_PG_DSN to win over PG_DSN", cfg.Postgres.DSN)
	}
}

func TestEnv_NestedStructFallsBackToTagName(t *testing.T) {
	t.Setenv("PG_DSN", "via-tag")

	var cfg nestedConfig
	if err := config.Load(context.Background(), &cfg, config.Env()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Postgres.DSN != "via-tag" {
		t.Fatalf("DSN = %q, want fallback to PG_DSN", cfg.Postgres.DSN)
	}
}

func TestEnv_ParseErrorIsWrappedWithIndex(t *testing.T) {
	t.Setenv("WORKERS", "many")

	var cfg appConfig
	err := config.Load(context.Background(), &cfg, config.Env())
	if err == nil || !strings.Contains(err.Error(), "config: source[0]") {
		t.Fatalf("err = %v, want wrapped with source index", err)
	}
}

type sourceFunc func(ctx context.Context, dst any) error

func (f sourceFunc) Apply(ctx context.Context, dst any) error { return f(ctx, dst) }

func TestLoad_LaterSourceOverridesEarlier(t *testing.T) {
	t.Setenv("ADDR", ":from-env")

	override := sourceFunc(func(_ context.Context, dst any) error {
		dst.(*appConfig).Addr = ":from-override"
		return nil
	})

	var cfg appConfig
	if err := config.Load(context.Background(), &cfg, config.Env(), override); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":from-override" {
		t.Fatalf("Addr = %q, want later source to win", cfg.Addr)
	}

	cfg = appConfig{}
	if err := config.Load(context.Background(), &cfg, override, config.Env()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":from-env" {
		t.Fatalf("Addr = %q, want env (applied last) to win", cfg.Addr)
	}
}

func TestLoad_AggregatesErrorsOfAllSources(t *testing.T) {
	first := errors.New("first")
	second := errors.New("second")
	applied := 0
	ok := sourceFunc(func(context.Context, any) error { applied++; return nil })

	var cfg appConfig
	err := config.Load(context.Background(), &cfg,
		sourceFunc(func(context.Context, any) error { return first }),
		ok,
		sourceFunc(func(context.Context, any) error { return second }),
	)
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("err = %v, want both source errors joined", err)
	}
	if applied != 1 {
		t.Fatalf("sources after a failing one must still run, applied = %d", applied)
	}
	for _, want := range []string{"source[0]", "source[2]"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want index %s", err, want)
		}
	}
}

func TestLoad_NoSourcesLeavesDstUntouched(t *testing.T) {
	cfg := appConfig{Addr: ":kept"}
	if err := config.Load(context.Background(), &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":kept" {
		t.Fatalf("Addr = %q, want untouched", cfg.Addr)
	}
}

func TestLoad_NonPointerDstIsError(t *testing.T) {
	if err := config.Load(context.Background(), appConfig{}, config.Env()); err == nil {
		t.Fatal("Load(non-pointer) = nil, want error")
	}
}

func TestModule_ProvidesSingleEnvSource(t *testing.T) {
	t.Setenv("ADDR", ":from-module")

	var srcs []config.Source
	app := fxtest.New(t, config.Module, fx.Populate(&srcs))
	app.RequireStart().RequireStop()

	if len(srcs) != 1 {
		t.Fatalf("len(sources) = %d, want 1", len(srcs))
	}

	var cfg appConfig
	if err := config.Load(context.Background(), &cfg, srcs...); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":from-module" {
		t.Fatalf("Addr = %q, want the provided source to read env", cfg.Addr)
	}
}
