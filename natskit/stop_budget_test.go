package natskit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

// Регрессия RFX-01: обработчик в полёте на остановке не съедает бюджет fx. Drain подписки и
// закрытие шины берут по две трети остатка: Stop возвращает nil, ctx обработчика отменён, хук
// после шины выполнен на живом ctx, соединение закрыто.
func TestModule_HandlerInFlightLeavesStopBudget(t *testing.T) {
	url := natsURL(t)
	for name, kind := range map[string]string{
		"jetstream":                          "stream",
		"core":                               "core",
		"jetstream without SubscribeOnStart": "direct",
	} {
		t.Run(name, func(t *testing.T) {
			stream := nameFor(t)
			cfg := jsConfig(stream)
			cfg.URL, cfg.ConnectTimeout, cfg.ReconnectWait, cfg.MaxReconnects = url, 5*time.Second, 100*time.Millisecond, 5
			cfg.DrainTimeout, cfg.HandlerTimeout = 10*time.Second, 30*time.Second
			cfg.JetStream.AckWait = 30 * time.Second
			cfg.Backpressure.MaxInFlight = 4

			started := make(chan struct{}, 1)
			cancelled := make(chan struct{})
			handler := func(ctx context.Context, _ []byte) error {
				started <- struct{}{}
				select {
				case <-ctx.Done():
					close(cancelled)
					return ctx.Err()
				case <-time.After(20 * time.Second):
					return nil
				}
			}
			log := &memLogger{}
			var later atomic.Bool
			var client *natskit.Client
			app := fx.New(fx.NopLogger,
				// Хук, зарегистрированный раньше шины, останавливается после неё — как пул и telemetry.
				fx.Invoke(func(lc fx.Lifecycle) {
					lc.Append(fx.Hook{OnStop: func(ctx context.Context) error {
						later.Store(ctx.Err() == nil)
						return nil
					}})
				}),
				fx.Supply(cfg, fx.Annotate(log, fx.As(new(logkit.Logger)))),
				natskit.Module(""),
				fx.Invoke(func(lc fx.Lifecycle, sb natskit.StreamBus, b natskit.Bus) error {
					subscribe := func(ctx context.Context) (natskit.Subscription, error) {
						if kind == "core" {
							return b.Subscribe(ctx, stream+".work", handler)
						}
						return sb.SubscribeStream(ctx, stream, stream+".>", "worker", handler)
					}
					if kind == "direct" {
						_, err := subscribe(context.Background())
						return err
					}
					natskit.SubscribeOnStart(lc, subscribe)
					return nil
				}),
				fx.Populate(&client))
			ctx := context.Background()
			if err := app.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(client.Close)
			var err error
			if kind == "core" {
				err = client.Bus().Publish(ctx, stream+".work", []byte(`{}`))
			} else {
				err = client.Stream().PublishStream(ctx, stream+".work", []byte(`{}`))
			}
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
			recv(t, started, 10*time.Second, "handler start")

			stopCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			begin := time.Now()
			if err := app.Stop(stopCtx); err != nil {
				t.Errorf("Stop = %v, want nil: a handler in flight is not a stop failure", err)
			}
			if elapsed := time.Since(begin); elapsed > 2900*time.Millisecond {
				t.Errorf("Stop took %s of a 3s budget: nothing is left to later hooks", elapsed)
			}
			if !later.Load() {
				t.Error("the hook after the bus did not run on a live ctx")
			}
			recv(t, cancelled, 2*time.Second, "handler ctx cancellation")
			if status := client.Conn().Status(); status != natsgo.CLOSED {
				t.Errorf("connection after Stop: %s, want CLOSED", status)
			}
			want := "natskit: subscription drain timeout, handlers cancelled"
			if kind == "direct" {
				want = "natskit: drain timeout, handlers still in flight"
			}
			if !log.has("warn", want) {
				t.Errorf("no %q in the log: %v", want, log.dump())
			}
		})
	}
}

// Unsubscribe не ждёт обработчиков и не отменяет их ctx: ctx подписки освобождается, когда
// вернулся последний из них, а не в момент снятия интереса.
func TestSubscription_UnsubscribeKeepsHandlerContext(t *testing.T) {
	conn, err := natskit.NewConn(clientConfig(natsURL(t)), logkit.Nop())
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	t.Cleanup(conn.Close)
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
	subject := subj(t, "work")

	started, unsubscribed := make(chan struct{}, 1), make(chan struct{})
	ctxErr := make(chan error, 1)
	sub, err := bus.Subscribe(context.Background(), subject, func(ctx context.Context, _ []byte) error {
		started <- struct{}{}
		<-unsubscribed
		ctxErr <- ctx.Err()
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(context.Background(), subject, []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, started, 5*time.Second, "handler start")
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	close(unsubscribed)
	if err := recv(t, ctxErr, 5*time.Second, "handler ctx state"); err != nil {
		t.Fatalf("handler ctx after Unsubscribe: %v, want a live ctx", err)
	}
}
