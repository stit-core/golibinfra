package natskit

import (
	"context"
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module поднимает инстанс шины: *Client, Bus и StreamBus. Config провайдит
// caller — `fx.Provide(natskit.NewConfig)` для дефолтного инстанса, для
// именованного `natskit.ProvideConfig(name)` (конфиг из `<NAME>_NATS_*` под
// fx-тегом `name:"<name>"`). Пустое имя — дефолтный инстанс, provide без тега.
func Module(name string) fx.Option {
	if name == "" {
		return fx.Module(
			"natskit",
			fx.Provide(
				newDefaultClient,
				func(c *Client) Bus { return c.Bus() },
				func(c *Client) StreamBus { return c.Stream() },
			),
		)
	}
	if !validInstanceName(name) {
		return fx.Error(fmt.Errorf("%w: instance %q: allowed characters are letters, digits, '_', '-', '.'", ErrConfig, name))
	}

	tag := nameTag(name)
	return fx.Module(
		"natskit:"+name,
		fx.Provide(
			fx.Annotate(
				func(lc fx.Lifecycle, cfg Config, log logkit.Logger) (*Client, error) {
					return newClient(lc, name, cfg, log)
				},
				fx.ParamTags(``, tag, ``), fx.ResultTags(tag),
			),
			fx.Annotate(
				func(c *Client) Bus { return c.Bus() },
				fx.ParamTags(tag), fx.ResultTags(tag),
			),
			fx.Annotate(
				func(c *Client) StreamBus { return c.Stream() },
				fx.ParamTags(tag), fx.ResultTags(tag),
			),
		),
	)
}

// ProvideConfig регистрирует конфиг именованного инстанса (`<NAME>_NATS_*`) под
// fx-тегом `name:"<name>"` — форма, которую ждёт Module(name). Пустое имя —
// эквивалент `fx.Provide(natskit.NewConfig)`.
func ProvideConfig(name string) fx.Option {
	if name == "" {
		return fx.Provide(NewConfig)
	}
	if !validInstanceName(name) {
		return fx.Error(fmt.Errorf("%w: instance %q: allowed characters are letters, digits, '_', '-', '.'", ErrConfig, name))
	}
	return fx.Provide(fx.Annotate(
		func() (Config, error) { return NewInstanceConfig(name) },
		fx.ResultTags(nameTag(name)),
	))
}

// HealthProbe регистрирует пробу инстанса в fx-группе `healthkit.probes`. Отдельной
// опцией, а не внутри Module: caller решает, какие инстансы валят readiness.
func HealthProbe(name string) fx.Option {
	if name == "" {
		return healthkit.Provide(Probe)
	}
	return healthkit.Provide(Probe, fx.ParamTags(nameTag(name)))
}

// SubscribeOnStart вешает подписку на жизненный цикл fx: subscribe вызывается в
// OnStart — после старта зависимостей, объявленных раньше, — с ctx хука (он
// ограничивает только сам вызов); ошибка подписки, включая ErrJetStreamDisabled,
// валит старт. OnStop дренит подписку в пределах своего ctx — до остановки
// этих зависимостей. Вызывается из fx.Invoke или конструктора: хук, добавленный
// из чужого OnStart, fx не запускает.
func SubscribeOnStart(lc fx.Lifecycle, subscribe func(ctx context.Context) (Subscription, error)) {
	var sub Subscription
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			s, err := subscribe(ctx)
			if err != nil {
				return fmt.Errorf("natskit: subscribe on start: %w", err)
			}
			sub = s
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if sub == nil {
				return nil
			}
			return sub.Drain(ctx)
		},
	})
}

func newDefaultClient(lc fx.Lifecycle, cfg Config, log logkit.Logger) (*Client, error) {
	return newClient(lc, DefaultInstance, cfg, log)
}

func newClient(lc fx.Lifecycle, name string, cfg Config, log logkit.Logger) (*Client, error) {
	c, err := New(name, cfg, log)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			c.CloseContext(ctx)
			return nil
		},
	})
	return c, nil
}

func nameTag(name string) string { return fmt.Sprintf(`name:"%s"`, name) }
