package config_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/config"
)

type appConfig struct {
	Addr    string   `envconfig:"ADDR" default:":8080"`
	Workers int      `envconfig:"WORKERS" default:"2"`
	Debug   bool     `envconfig:"DEBUG"`
	Origins []string `envconfig:"ORIGINS" default:"https://a.example"`
	Name    string   `ignored:"true"`
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
	if err := config.Load(&cfg, config.Env()); err != nil {
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
	if err := config.Load(&cfg, config.EnvPrefixed("cfgtest")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":7070" {
		t.Fatalf("Addr = %q, want value from CFGTEST_ADDR", cfg.Addr)
	}
}

func TestEnvPrefixed_FallsBackToUnprefixed(t *testing.T) {
	t.Setenv("ADDR", ":unprefixed")

	var cfg appConfig
	if err := config.Load(&cfg, config.EnvPrefixed("cfgtest")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":unprefixed" {
		t.Fatalf("Addr = %q, want fallback to unprefixed ADDR", cfg.Addr)
	}
}

// Пустая переменная равна незаданной: Helm `value: ""` и compose без подстановки дают дефолт.
func TestEnv_EmptyValueIsUnset(t *testing.T) {
	t.Setenv("ADDR", "")
	t.Setenv("WORKERS", "")
	t.Setenv("ORIGINS", "")

	var cfg appConfig
	if err := config.Load(&cfg, config.Env()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8080" || cfg.Workers != 2 || len(cfg.Origins) != 1 {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}
}

func TestEnv_EmptyMarkerSetsEmptyList(t *testing.T) {
	t.Setenv("ORIGINS", "-")

	var cfg appConfig
	if err := config.Load(&cfg, config.Env()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Origins != nil {
		t.Fatalf("Origins = %v, want an explicit empty list", cfg.Origins)
	}
}

// Поле без тега из ENV не читается, поэтому без ignored:"true" это ошибка, а не тихий ноль.
func TestEnv_UntaggedFieldIsAnError(t *testing.T) {
	type app struct {
		Region string
	}
	t.Setenv("REGION", "eu")
	err := config.Load(&app{}, config.Env())
	if err == nil || !strings.Contains(err.Error(), "app.Region has no envconfig tag") {
		t.Fatalf("err = %v, want an error naming the untagged field", err)
	}
}

// Вложенная структура читается теми же именами тегов: теневых имён с приставкой поля нет.
func TestEnv_NestedStructUsesTagNames(t *testing.T) {
	t.Setenv("PG_DSN", "via-tag")
	t.Setenv("POSTGRES_PG_DSN", "shadow")

	var cfg nestedConfig
	if err := config.Load(&cfg, config.Env()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Postgres.DSN != "via-tag" {
		t.Fatalf("DSN = %q, want PG_DSN", cfg.Postgres.DSN)
	}

	t.Setenv("CFGTEST_PG_DSN", "prefixed")
	cfg = nestedConfig{}
	if err := config.Load(&cfg, config.EnvPrefixed("cfgtest")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Postgres.DSN != "prefixed" {
		t.Fatalf("DSN = %q, want CFGTEST_PG_DSN", cfg.Postgres.DSN)
	}
}

// Регрессия B1 валидатора A: тег у вложенной структуры App — ошибка Load с именем поля, а не
// молча пустая секция без дефолтов.
func TestEnv_TaggedNestedStructIsAnError(t *testing.T) {
	type app struct {
		DB pgSection `envconfig:"DB"`
	}
	t.Setenv("DB_PG_DSN", "postgres://tagged")
	err := config.Load(&app{}, config.Env())
	if err == nil || !strings.Contains(err.Error(), "config: source[0]: field app.DB: envconfig tag on a nested struct") {
		t.Fatalf("err = %v, want an error naming the tagged nested struct", err)
	}
}

// Тип, которого ридер не разбирает, — ошибка Load с именем поля и типом, а не совет снять тег:
// у url.URL только UnmarshalBinary.
func TestEnv_UnsupportedTypeIsAnError(t *testing.T) {
	type app struct {
		Endpoint url.URL `envconfig:"ENDPOINT"`
	}
	t.Setenv("ENDPOINT", "https://api.example")
	err := config.Load(&app{}, config.Env())
	if err == nil || !strings.HasPrefix(err.Error(), "config: source[0]: field app.Endpoint: unsupported type url.URL: ") {
		t.Fatalf("err = %v, want an unsupported type error naming the field", err)
	}
}

// Две секции одного кита: вторая помечена ignored и читается отдельным Load с приставкой —
// рецепт config/README.md, иначе обе секции читали бы PG_DSN.
func TestEnvPrefixed_SecondSectionOfOneKit(t *testing.T) {
	type app struct {
		Primary   pgSection
		Analytics pgSection `ignored:"true"`
	}
	t.Setenv("PG_DSN", "postgres://primary")
	t.Setenv("ANALYTICS_PG_DSN", "postgres://analytics")

	var cfg app
	if err := config.Load(&cfg, config.Env()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := config.Load(&cfg.Analytics, config.EnvPrefixed("analytics")); err != nil {
		t.Fatalf("Load second section: %v", err)
	}
	if cfg.Primary.DSN != "postgres://primary" || cfg.Analytics.DSN != "postgres://analytics" {
		t.Fatalf("sections = %+v, want PG_DSN and ANALYTICS_PG_DSN", cfg)
	}
}

func TestEnv_ParseErrorNamesSourceAndVariable(t *testing.T) {
	t.Setenv("WORKERS", "many")

	var cfg appConfig
	err := config.Load(&cfg, config.Env())
	if err == nil || !strings.Contains(err.Error(), "config: source[0]: WORKERS: ") {
		t.Fatalf("err = %v, want wrapped with source index and the variable", err)
	}
}

type sourceFunc func(dst any) error

func (f sourceFunc) Apply(dst any) error { return f(dst) }

func TestLoad_LaterSourceOverridesEarlier(t *testing.T) {
	t.Setenv("ADDR", ":from-env")

	override := sourceFunc(func(dst any) error {
		dst.(*appConfig).Addr = ":from-override"
		return nil
	})

	var cfg appConfig
	if err := config.Load(&cfg, config.Env(), override); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":from-override" {
		t.Fatalf("Addr = %q, want later source to win", cfg.Addr)
	}

	cfg = appConfig{}
	if err := config.Load(&cfg, override, config.Env()); err != nil {
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
	ok := sourceFunc(func(any) error { applied++; return nil })

	var cfg appConfig
	err := config.Load(&cfg,
		sourceFunc(func(any) error { return first }),
		ok,
		sourceFunc(func(any) error { return second }),
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
	if err := config.Load(&cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":kept" {
		t.Fatalf("Addr = %q, want untouched", cfg.Addr)
	}
}

func TestLoad_NonPointerDstIsError(t *testing.T) {
	if err := config.Load(appConfig{}, config.Env()); err == nil {
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
	if err := config.Load(&cfg, srcs...); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":from-module" {
		t.Fatalf("Addr = %q, want the provided source to read env", cfg.Addr)
	}
}

// Граф из README: App-конфиг грузится провайдером без ctx в графе.
func TestModule_AppConfigWithoutCtxInGraph(t *testing.T) {
	t.Setenv("ADDR", ":from-graph")

	var got *appConfig
	app := fxtest.New(t,
		config.Module,
		fx.Provide(func(srcs []config.Source) (*appConfig, error) {
			var cfg appConfig
			return &cfg, config.Load(&cfg, srcs...)
		}),
		fx.Populate(&got),
	)
	app.RequireStart().RequireStop()
	if got == nil || got.Addr != ":from-graph" {
		t.Fatalf("App config = %+v, want Addr from the environment", got)
	}
}
