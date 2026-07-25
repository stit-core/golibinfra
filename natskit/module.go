package natskit

import (
	"context"
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module поднимает инстанс шины: Config, *Client, Bus и StreamBus. Пустое имя —
// дефолтный инстанс (конфиг из `NATS_*`, provide без тега). Именованный инстанс
// читает `<NAME>_NATS_*` и provide'ится под fx-тегом `name:"<name>"` — так в
// одном графе живут несколько шин.
func Module(name string) fx.Option {
	if name == "" {
		return fx.Module(
			"natskit",
			fx.Provide(
				NewConfig,
				newDefaultClient,
				func(c *Client) Bus { return c.Bus() },
				func(c *Client) StreamBus { return c.Stream() },
			),
		)
	}

	tag := nameTag(name)
	prefix := envPrefix(name)
	return fx.Module(
		"natskit:"+name,
		fx.Provide(
			fx.Annotate(
				func() (Config, error) { return NewConfigPrefixed(prefix) },
				fx.ResultTags(tag),
			),
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

// HealthProbe регистрирует пробу инстанса в `group:"health"`. Отдельной опцией,
// потому что тип пробы (`func(context.Context) error`) коллизит с пробами других
// китов — caller решает, какие инстансы попадают в health.
func HealthProbe(name string) fx.Option {
	if name == "" {
		return fx.Provide(fx.Annotate(Probe, fx.ResultTags(`group:"health"`)))
	}
	return fx.Provide(fx.Annotate(
		Probe,
		fx.ParamTags(nameTag(name)), fx.ResultTags(`group:"health"`),
	))
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
		OnStop: func(context.Context) error {
			c.Close()
			return nil
		},
	})
	return c, nil
}

func nameTag(name string) string { return fmt.Sprintf(`name:"%s"`, name) }
