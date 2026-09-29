package natskit_test

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestModule_DefaultAndNamedInstancesCoexist(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(logkit.Nop),
		fx.Provide(natskit.NewConfig),
		natskit.Module(""),
		natskit.ProvideConfig("hub"),
		natskit.Module("hub"),
		natskit.HealthProbe(""),
		fx.Invoke(fx.Annotate(
			func(local, hub natskit.StreamBus, hubBus natskit.Bus, hubClient *natskit.Client) {},
			fx.ParamTags(``, `name:"hub"`, `name:"hub"`, `name:"hub"`),
		)),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// Module не provide'ит Config: без fx.Provide(natskit.NewConfig) граф не собирается.
func TestModule_RequiresCallerProvidedConfig(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(logkit.Nop),
		natskit.Module(""),
		fx.Invoke(func(natskit.Bus) {}),
	)
	if err == nil {
		t.Fatal("ValidateApp = nil, want missing natskit.Config")
	}
}

func TestModule_NamedInstanceHealthProbe(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(logkit.Nop),
		natskit.ProvideConfig("hub"),
		natskit.Module("hub"),
		natskit.HealthProbe("hub"),
		fx.Invoke(fx.Annotate(
			func(probes []healthkit.Probe) {},
			fx.ParamTags(`group:"`+healthkit.Group+`"`),
		)),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// Probe совместима с healthkit.Provide — путь, которым пробы регистрируют остальные киты.
func TestModule_ProbeViaHealthkitProvide(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(logkit.Nop),
		fx.Provide(natskit.NewConfig),
		natskit.Module(""),
		healthkit.Provide(natskit.Probe),
		fx.Invoke(fx.Annotate(
			func(probes []healthkit.Probe) {},
			fx.ParamTags(`group:"`+healthkit.Group+`"`),
		)),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// Имя инстанса, из которого нельзя собрать префикс ENV, валит сборку графа.
func TestModule_RejectsInvalidInstanceName(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(logkit.Nop),
		natskit.Module("hub/v2"),
	)
	if !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("ValidateApp = %v, want ErrConfig", err)
	}
	if err := fx.ValidateApp(natskit.ProvideConfig("hub/v2")); !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("ProvideConfig = %v, want ErrConfig", err)
	}
}

// Живой граф: Module поднимает клиента и закрывает его в OnStop.
func TestModule_StartsAndStopsClient(t *testing.T) {
	url := natsURL(t)
	t.Setenv("NATS_URL", url)

	var client *natskit.Client
	app := fx.New(
		fx.NopLogger,
		fx.Provide(logkit.Nop),
		fx.Provide(natskit.NewConfig),
		natskit.Module(""),
		fx.Populate(&client),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx.New: %v", err)
	}
	ctx := t.Context()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := client.Healthy(); err != nil {
		t.Fatalf("Healthy: %v", err)
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := client.Healthy(); !errors.Is(err, natskit.ErrConnectionClosed) {
		t.Fatalf("Healthy after Stop = %v, want ErrConnectionClosed", err)
	}
}

// drainSub — Subscription, которая сообщает о Drain.
type drainSub struct{ onDrain func() }

func (drainSub) Unsubscribe() error { return nil }

func (s drainSub) Drain(context.Context) error {
	s.onDrain()
	return nil
}

// Регрессия COD-06: жизненный цикл подписки — одна строка. Подписка идёт в
// OnStart после старта зависимостей, объявленных раньше, и дренится в OnStop
// до их остановки.
func TestSubscribeOnStart_SubscribesOnStartDrainsOnStop(t *testing.T) {
	var events []string
	record := func(e string) func() { return func() { events = append(events, e) } }
	app := fxtest.New(t,
		fx.Invoke(func(lc fx.Lifecycle) {
			lc.Append(fx.StartStopHook(record("dependency started"), record("dependency stopped")))
		}),
		fx.Invoke(func(lc fx.Lifecycle) {
			natskit.SubscribeOnStart(lc, func(ctx context.Context) (natskit.Subscription, error) {
				if ctx == nil {
					t.Error("subscribe got a nil ctx")
				}
				record("subscribed")()
				return drainSub{onDrain: record("drained")}, nil
			})
		}),
	)
	if len(events) != 0 {
		t.Fatalf("events before start: %v", events)
	}
	app.RequireStart()
	app.RequireStop()

	want := []string{"dependency started", "subscribed", "drained", "dependency stopped"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
}

// Выключенный JetStream — ошибка старта, а не молча не работающий подписчик.
func TestSubscribeOnStart_JetStreamDisabledFailsStart(t *testing.T) {
	sb, err := natskit.NewStreamBus(nil, logkit.Nop(), natskit.Config{})
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	app := fx.New(fx.NopLogger, fx.Invoke(func(lc fx.Lifecycle) {
		natskit.SubscribeOnStart(lc, func(ctx context.Context) (natskit.Subscription, error) {
			return natskit.SubscribeStreamJSON(sb, ctx, "orders", "orders.created", "billing",
				func(context.Context, struct{}) error { return nil })
		})
	}))
	if err := app.Start(t.Context()); !errors.Is(err, natskit.ErrJetStreamDisabled) {
		t.Fatalf("Start = %v, want ErrJetStreamDisabled", err)
	}
}
