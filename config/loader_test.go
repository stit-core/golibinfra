package config_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/config"
)

type appConfig struct {
	Addr    string `envconfig:"ADDR" default:":8080"`
	Workers int    `envconfig:"WORKERS" default:"2"`
	Debug   bool   `envconfig:"DEBUG"`
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

func TestEnv_Prefix(t *testing.T) {
	t.Setenv("APP_ADDR", ":7070")
	t.Setenv("ADDR", ":1")

	var cfg appConfig
	if err := config.Load(context.Background(), &cfg, config.Env("app")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":7070" {
		t.Fatalf("Addr = %q, want value from APP_ADDR", cfg.Addr)
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

func TestLoad_StopsAtFirstFailingSource(t *testing.T) {
	boom := errors.New("boom")
	calls := 0
	failing := sourceFunc(func(context.Context, any) error { return boom })
	counting := sourceFunc(func(context.Context, any) error { calls++; return nil })

	var cfg appConfig
	err := config.Load(context.Background(), &cfg, counting, failing, counting)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "source[1]") {
		t.Fatalf("err = %v, want boom at source[1]", err)
	}
	if calls != 1 {
		t.Fatalf("sources after the failing one must not run, calls = %d", calls)
	}
}
