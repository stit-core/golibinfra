package natskit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

const (
	msgStreamNotFound = "natskit: stream not found, waiting for its author to create it"
	msgStreamAppeared = "natskit: awaited stream appeared, subscribed"
)

// waitingBus — шина без своих стримов с отступом ожидания потока first → 100ms. Снимок метрик — до шины: провайдер
// процесса ставит первый Collect, а измерение до него теряется.
func waitingBus(t *testing.T, conn *natskit.Conn, log logkit.Logger, first, remind time.Duration) natskit.StreamBus {
	t.Helper()
	metrictest.Collect(t)
	sb, err := natskit.NewStreamBus(jsConfig(), conn, log)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	natskit.ExportStreamWait(sb, first, max(first, 100*time.Millisecond), remind)
	return sb
}

// waiting — подписки шины в ожидании потока stream (gauge nats_subscriptions_waiting).
func waiting(t *testing.T, stream string) float64 {
	t.Helper()
	return metrictest.Collect(t).Value("nats_subscriptions_waiting", "stream", stream, "transport", "jetstream")
}

func jetStreamAPI(t *testing.T) jetstream.JetStream {
	t.Helper()
	nc, err := natsgo.Connect(natsURL(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	return js
}

// Подписка на поток, которого нет, не отказывает: шина ждёт поток (WARN, gauge ожидания), и, когда автор заводит
// поток, подписка получает события с его начала — и опубликованные до того, как консьюмер встал.
func TestStreamWait_SubscribesWhenStreamAppears(t *testing.T) {
	t.Parallel()
	stream := nameFor(t)
	log := &memLogger{}
	sb := waitingBus(t, connect(t, natsURL(t)), log, 20*time.Millisecond, time.Hour)
	got := make(chan string, 4)
	sub, err := sb.SubscribeStream(t.Context(), stream, stream+".>", "reader", func(_ context.Context, payload []byte) error {
		got <- string(payload)
		return nil
	})
	if err != nil {
		t.Fatalf("SubscribeStream on a missing stream: %v, want a waiting subscription", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	eventually(t, 5*time.Second, "WARN of the wait", func() bool { return log.has("warn", msgStreamNotFound) })
	if e := log.find("warn", msgStreamNotFound); e.value("stream") != stream || e.value("durable") != "reader" {
		t.Fatalf("the WARN does not name the stream and the durable: %s", e.text())
	}
	eventually(t, 5*time.Second, "the gauge of the wait", func() bool { return waiting(t, stream) == 1 })

	author := newStreamBus(t, jsConfig(stream))
	for _, payload := range []string{"first", "second"} {
		if err := author.PublishStream(t.Context(), subj(t, "created"), []byte(payload)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	// Обработчики шины идут параллельно: порядок доставки обработчику не обещан.
	delivered := map[string]bool{}
	for range 2 {
		delivered[recv(t, got, 10*time.Second, "an event")] = true
	}
	if !delivered["first"] || !delivered["second"] {
		t.Fatalf("delivered %v, want both events published before the consumer", delivered)
	}
	if !log.has("info", msgStreamAppeared) {
		t.Fatalf("no INFO of the subscription: %v", log.dump())
	}
	eventually(t, 5*time.Second, "the gauge after the stream appeared", func() bool { return waiting(t, stream) == 0 })
}

// Ожидание снимают Unsubscribe, Drain и закрытие шины: gauge — к нулю, durable свободен для другого фильтра, а
// поток, заведённый позже, консьюмера снятой подписки не получает.
func TestStreamWait_StopWhileWaiting(t *testing.T) {
	t.Parallel()
	url := natsURL(t)
	for _, how := range []string{"unsubscribe", "drain", "close"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			stream := nameFor(t)
			conn := connect(t, url)
			sb := waitingBus(t, conn, logkit.Nop(), 20*time.Millisecond, time.Hour)
			handler := func(context.Context, []byte) error { return nil }
			sub, err := sb.SubscribeStream(t.Context(), stream, stream+".a", "reader", handler)
			if err != nil {
				t.Fatalf("SubscribeStream: %v", err)
			}
			eventually(t, 5*time.Second, "the gauge of the wait", func() bool { return waiting(t, stream) == 1 })

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			begin := time.Now()
			switch how {
			case "unsubscribe":
				err = sub.Unsubscribe()
			case "drain":
				err = sub.Drain(ctx)
			case "close":
				conn.CloseContext(ctx)
			}
			if err != nil || time.Since(begin) > 2*time.Second {
				t.Fatalf("%s while waiting: %v in %s", how, err, time.Since(begin))
			}
			eventually(t, 5*time.Second, "the gauge after the stop", func() bool { return waiting(t, stream) == 0 })

			if how != "close" {
				again, err := sb.SubscribeStream(t.Context(), stream, stream+".b", "reader", handler)
				if err != nil {
					t.Fatalf("the durable is still held by the stopped wait: %v", err)
				}
				if err := again.Unsubscribe(); err != nil {
					t.Fatalf("unsubscribe: %v", err)
				}
			}
			newStreamBus(t, jsConfig(stream))
			time.Sleep(300 * time.Millisecond)
			if _, err := jetStreamAPI(t).Consumer(t.Context(), stream, "reader"); !errors.Is(err, jetstream.ErrConsumerNotFound) {
				t.Fatalf("a stopped wait created its consumer: %v", err)
			}
		})
	}
}

// Поток появился, но консьюмер не заводится (durable занят консьюмером с другой политикой доставки) — подписка пишет
// WARN с ошибкой и ждёт дальше; durable освободился — подписка встаёт.
func TestStreamWait_RetriesOtherErrors(t *testing.T) {
	t.Parallel()
	stream := nameFor(t)
	log := &memLogger{}
	// Первая повторная попытка — через 2s: тест успевает завести поток и занять durable до неё.
	sb := waitingBus(t, connect(t, natsURL(t)), log, 2*time.Second, time.Hour)
	got := make(chan string, 1)
	sub, err := sb.SubscribeStream(t.Context(), stream, subj(t, "x"), "reader", func(_ context.Context, payload []byte) error {
		got <- string(payload)
		return nil
	})
	if err != nil {
		t.Fatalf("SubscribeStream: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	js := jetStreamAPI(t)
	if _, err := js.CreateStream(t.Context(), jetstream.StreamConfig{Name: stream, Subjects: []string{stream + ".>"}}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if _, err := js.CreateOrUpdateConsumer(t.Context(), stream, jetstream.ConsumerConfig{
		Durable: "reader", AckPolicy: jetstream.AckExplicitPolicy, DeliverPolicy: jetstream.DeliverNewPolicy, FilterSubject: subj(t, "x"),
	}); err != nil {
		t.Fatalf("take the durable: %v", err)
	}
	eventually(t, 10*time.Second, "WARN of the failed attempt", func() bool {
		return log.has("warn", "natskit: subscribe to the awaited stream failed, retrying")
	})
	if log.has("info", msgStreamAppeared) {
		t.Fatal("subscribed over a taken durable")
	}
	if err := js.DeleteConsumer(t.Context(), stream, "reader"); err != nil {
		t.Fatalf("free the durable: %v", err)
	}
	eventually(t, 10*time.Second, "INFO of the subscription", func() bool { return log.has("info", msgStreamAppeared) })
	if _, err := js.Publish(t.Context(), subj(t, "x"), []byte("event")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := recv(t, got, 10*time.Second, "event"); got != "event" {
		t.Fatalf("delivered %q", got)
	}
}

// Долгое ожидание повторяет WARN с тем, сколько подписка уже ждёт.
func TestStreamWait_Reminds(t *testing.T) {
	t.Parallel()
	stream := nameFor(t)
	log := &memLogger{}
	sb := waitingBus(t, connect(t, natsURL(t)), log, 20*time.Millisecond, 50*time.Millisecond)
	sub, err := sb.SubscribeStream(t.Context(), stream, stream+".>", "reader", func(context.Context, []byte) error { return nil })
	if err != nil {
		t.Fatalf("SubscribeStream: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	eventually(t, 5*time.Second, "a reminder", func() bool { return log.has("warn", "natskit: still waiting for stream") })
	if e := log.find("warn", "natskit: still waiting for stream"); e.value("waited") == nil || e.value("stream") != stream {
		t.Fatalf("the reminder: %s", e.text())
	}
}

// Сервис с подпиской на поток соседа, которого ещё нет, стартует; остановка снимает ожидание.
func TestStreamWait_SubscribeOnStartDoesNotFailStart(t *testing.T) {
	t.Parallel()
	stream := nameFor(t)
	cfg := clientConfig(natsURL(t))
	cfg.JetStream = jsConfig().JetStream
	metrictest.Collect(t)
	app := fxtest.New(t,
		fx.Supply(cfg),
		fx.Provide(logkit.Nop),
		natskit.Module(""),
		fx.Invoke(func(lc fx.Lifecycle, sb natskit.StreamBus) {
			natskit.SubscribeOnStart(lc, func(ctx context.Context) (natskit.Subscription, error) {
				return sb.SubscribeStream(ctx, stream, stream+".>", "reader", func(context.Context, []byte) error { return nil })
			})
		}),
	)
	app.RequireStart()
	eventually(t, 5*time.Second, "the gauge of the wait", func() bool { return waiting(t, stream) == 1 })
	app.RequireStop()
	if n := waiting(t, stream); n != 0 {
		t.Fatalf("waiting after stop: %v", n)
	}
}
