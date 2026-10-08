package healthkit_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
)

func TestProvideProbe_LandsInHealthGroup(t *testing.T) {
	type in struct {
		fx.In
		Probes []healthkit.Probe `group:"healthkit.probes"`
	}
	var got []healthkit.Probe
	app := fx.New(
		fx.NopLogger,
		healthkit.ProvideProbe(func() healthkit.Probe {
			return healthkit.New("pgkit", func(context.Context) error { return nil })
		}),
		healthkit.ProvideProbe(func() healthkit.Probe {
			return healthkit.New("natskit", func(context.Context) error { return errors.New("down") })
		}),
		fx.Invoke(func(i in) { got = i.Probes }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("probes in group: got %d, want 2", len(got))
	}
	for _, p := range got {
		if p.Name == "" {
			t.Fatal("probe in group must carry a name")
		}
	}
}

// Регрессия: конструктор неверного типа кладёт значение в группу другим типом,
// httpkit получает пустой срез и /ready остаётся «ok» без единой проверки.
func TestProvideProbe_RejectsWrongConstructor(t *testing.T) {
	tests := []struct {
		name string
		ctor any
	}{
		{name: "returns bare func", ctor: func() func(context.Context) error {
			return func(context.Context) error { return nil }
		}},
		{name: "returns error only", ctor: func() error { return nil }},
		{name: "returns nothing", ctor: func() {}},
		{name: "probe second", ctor: func() (error, healthkit.Probe) { return nil, healthkit.Probe{} }}, //nolint:staticcheck // проверяется именно неверный порядок результатов
		{name: "not a function", ctor: healthkit.Probe{}},
		{name: "nil", ctor: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fx.New(fx.NopLogger, healthkit.ProvideProbe(tt.ctor))
			err := app.Err()
			if err == nil {
				t.Fatal("want error from fx.New, got nil")
			}
			if !strings.Contains(err.Error(), "healthkit: ProvideProbe") {
				t.Fatalf("error must name healthkit.ProvideProbe: %v", err)
			}
		})
	}
}

func TestProvideProbe_AcceptsConstructorWithError(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		healthkit.ProvideProbe(func() (healthkit.Probe, error) {
			return healthkit.New("pgkit", func(context.Context) error { return nil }), nil
		}),
		fx.Invoke(fx.Annotate(
			func(probes []healthkit.Probe) {
				if len(probes) != 1 {
					t.Fatalf("probes: got %d, want 1", len(probes))
				}
			},
			fx.ParamTags(`group:"`+healthkit.ProbesGroup+`"`),
		)),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
}

// Регрессия: именованному инстансу нужен fx.ParamTags, и без вариадика киты
// писали групповой тег руками — мимо проверки конструктора.
func TestProvideProbe_WithAnnotations(t *testing.T) {
	type cfg struct{ name string }
	newProbe := func(c cfg) healthkit.Probe {
		return healthkit.New(c.name, func(context.Context) error { return nil })
	}

	t.Run("named param", func(t *testing.T) {
		var got []healthkit.Probe
		app := fx.New(
			fx.NopLogger,
			fx.Supply(fx.Annotated{Name: "events", Target: cfg{name: "natskit:events"}}),
			healthkit.ProvideProbe(newProbe, fx.ParamTags(`name:"events"`)),
			fx.Invoke(fx.Annotate(
				func(probes []healthkit.Probe) { got = probes },
				fx.ParamTags(`group:"`+healthkit.ProbesGroup+`"`),
			)),
		)
		if err := app.Err(); err != nil {
			t.Fatalf("fx: %v", err)
		}
		if len(got) != 1 || got[0].Name != "natskit:events" {
			t.Fatalf("probes = %v, want one named natskit:events", got)
		}
	})

	t.Run("wrong constructor still rejected", func(t *testing.T) {
		app := fx.New(
			fx.NopLogger,
			healthkit.ProvideProbe(func(cfg) error { return nil }, fx.ParamTags(`name:"events"`)),
		)
		err := app.Err()
		if err == nil || !strings.Contains(err.Error(), "healthkit: ProvideProbe") {
			t.Fatalf("err = %v, want the healthkit.ProvideProbe type check", err)
		}
	})
}

// Регрессия: Probe без Check — ошибка конфигурации графа с именем пробы, а не пойманная panic
// проверки: /ready пишет ту ERROR'ом со стеком.
func TestProbe_Run_NilCheck(t *testing.T) {
	err := healthkit.Probe{Name: "pgkit"}.Run(context.Background())
	if err == nil {
		t.Fatal("want error for a probe without Check")
	}
	var panicked *healthkit.PanicError
	if errors.As(err, &panicked) || !strings.Contains(err.Error(), `probe "pgkit": check is nil`) {
		t.Fatalf("error must name the probe and the missing check, not a recovered panic: %v", err)
	}
}

func TestProbe_Run(t *testing.T) {
	want := errors.New("pgkit: ping: refused")
	if err := healthkit.New("pgkit", func(context.Context) error { return want }).Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run: got %v, want %v", err, want)
	}
	if err := healthkit.New("pgkit", func(context.Context) error { return nil }).Run(context.Background()); err != nil {
		t.Fatalf("Run: got %v, want nil", err)
	}
}

func TestWithTimeout_HangingProbe(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	// Проба игнорирует ctx — таймаут обязан вернуть управление без неё.
	p := healthkit.WithTimeout(healthkit.New("stuck", func(context.Context) error {
		<-release
		return nil
	}), 20*time.Millisecond)

	// Свой дедлайн теста: без таймаута пробы Run не вернётся, и пакет завис бы до лимита go test.
	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background()) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return: WithTimeout must give control back after 20ms without the probe")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run: got %v, want context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("error must name the probe: %v", err)
	}
	if p.Name != "stuck" {
		t.Fatalf("name lost by WithTimeout: %q", p.Name)
	}
}

func TestWithTimeout_PassThrough(t *testing.T) {
	want := errors.New("down")
	p := healthkit.WithTimeout(healthkit.New("pgkit", func(context.Context) error { return want }), time.Minute)
	if err := p.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run: got %v, want %v", err, want)
	}

	nonPositive := healthkit.WithTimeout(healthkit.New("pgkit", func(context.Context) error { return nil }), 0)
	if err := nonPositive.Run(context.Background()); err != nil {
		t.Fatalf("Run: got %v, want nil", err)
	}
	if err := healthkit.WithTimeout(healthkit.Probe{Name: "pgkit"}, time.Second).Run(context.Background()); err == nil {
		t.Fatal("WithTimeout must keep the nil-Check error")
	}
}

func TestWithTimeout_PropagatesDeadlineToCheck(t *testing.T) {
	var deadlineSet bool
	p := healthkit.WithTimeout(healthkit.New("pgkit", func(ctx context.Context) error {
		_, deadlineSet = ctx.Deadline()
		return nil
	}), time.Minute)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !deadlineSet {
		t.Fatal("check must receive a context with a deadline")
	}
}

// panic проверки — ошибка пробы со значением и стеком, а не падение процесса: /ready и
// health-loop grpckit зовут пробы в своих горутинах, где recover вызывающего не поможет.
func TestProbe_Run_PanicIsError(t *testing.T) {
	boom := func(context.Context) error { panic("sync state is not initialised") }
	for name, p := range map[string]healthkit.Probe{
		"run":          healthkit.New("sync", boom),
		"with timeout": healthkit.WithTimeout(healthkit.New("sync", boom), time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			err := p.Run(context.Background())
			var panicked *healthkit.PanicError
			if !errors.As(err, &panicked) {
				t.Fatalf("Run: got %v, want *PanicError", err)
			}
			if panicked.Probe != "sync" || fmt.Sprint(panicked.Value) != "sync state is not initialised" ||
				!strings.Contains(string(panicked.Stack), "healthkit_test.go") {
				t.Fatalf("PanicError must carry the probe, the panic value and its stack: %+v", panicked)
			}
			if !strings.Contains(err.Error(), `"sync"`) {
				t.Fatalf("error must name the probe: %v", err)
			}
		})
	}
}
