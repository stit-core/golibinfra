package natskit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"no userinfo", "nats://localhost:4222", "nats://localhost:4222"},
		{"user and password", "nats://user:s3cret@nats:4222", "nats://xxxxx@nats:4222"},
		{"token", "nats://t0ken@nats:4222", "nats://xxxxx@nats:4222"},
		{"password with at", "nats://user:p@ss@nats:4222", "nats://xxxxx@nats:4222"},
		{"list", "nats://u:p@a:4222, nats://b:4222", "nats://xxxxx@a:4222,nats://b:4222"},
		{"no scheme", "localhost:4222", "localhost:4222"},
		{"no scheme with userinfo", "user:s3cret@nats:4222,t0ken@b:4222", "xxxxx@nats:4222,xxxxx@b:4222"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactURL(tt.raw); got != tt.want {
				t.Fatalf("redactURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// Фильтр stream-подписки проверяется до обращения к серверу: `>` не последним тот отверг бы
// асинхронно, и подписка молча осталась бы пустой.
func TestSubscribeStream_ChecksFilterBeforeTheServer(t *testing.T) {
	b := &jsBus{enabled: true}
	_, err := b.SubscribeStream(context.Background(), "ORDERS", "orders.>.x", "d", func(context.Context, []byte) error { return nil })
	if !errors.Is(err, nats.ErrBadSubject) {
		t.Fatalf("SubscribeStream with `>` not last = %v, want nats.ErrBadSubject", err)
	}
}

func TestSubjectMatches(t *testing.T) {
	tests := []struct {
		filter, subject string
		want            bool
	}{
		{"ORDERS.>", "ORDERS.dlq", true},
		{"ORDERS.>", "ORDERS.a.b", true},
		{"ORDERS.>", "ORDERS", false},
		{"ORDERS.*", "ORDERS.dlq", true},
		{"ORDERS.*", "ORDERS.dlq.deep", false},
		{"ORDERS.created", "ORDERS.created", true},
		{"ORDERS.created", "ORDERS.cancelled", false},
		{"a.*.cmd.>", "a.b.cmd.x", true},
		{"a.*.cmd.>", "a.b.evt.x", false},
		{">", "anything.at.all", true},
		// subject-фильтр: накрыт, только если накрыт каждый подходящий subject.
		{"ORDERS.>", "ORDERS.>", true},
		{"ORDERS.>", "ORDERS.*.created", true},
		{">", "ORDERS.>", true},
		{"ORDERS.*", "ORDERS.*", true},
		{"ORDERS.*", "ORDERS.>", false},
		{"ORDERS.created", "ORDERS.*", false},
		{"ORDERS.events.created", "ORDERS.>", false},
	}
	for _, tt := range tests {
		if got := SubjectMatches(tt.filter, tt.subject); got != tt.want {
			t.Errorf("SubjectMatches(%q, %q) = %v, want %v", tt.filter, tt.subject, got, tt.want)
		}
	}
}

func TestParseRetention(t *testing.T) {
	tests := []struct {
		in      string
		want    jetstream.RetentionPolicy
		wantErr bool
	}{
		{"", jetstream.LimitsPolicy, false},
		{"limits", jetstream.LimitsPolicy, false},
		{" Interest ", jetstream.InterestPolicy, false},
		{"workqueue", jetstream.WorkQueuePolicy, false},
		{"work_queue", jetstream.WorkQueuePolicy, false},
		{"nonsense", 0, true},
	}
	for _, tt := range tests {
		got, err := parseRetention(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("parseRetention(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Errorf("parseRetention(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestUnionSubjects(t *testing.T) {
	got := unionSubjects([]string{"A.>", "B.>"}, []string{"B.>", "C.>"})
	want := []string{"A.>", "B.>", "C.>"}
	if len(got) != len(want) {
		t.Fatalf("unionSubjects = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unionSubjects = %v, want %v", got, want)
		}
	}
}

func TestHeaderValueAndTruncate(t *testing.T) {
	if got := headerValue("boom\nsecond\rline"); got != "boom second line" {
		t.Fatalf("headerValue = %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("ab", 3); got != "ab" {
		t.Fatalf("truncate = %q", got)
	}
}

func TestPublishOptionsHeaders(t *testing.T) {
	caller := nats.Header{"Event-Type": []string{"order.created"}}
	po := NewPublishSpec(WithMsgID("id-1"), WithMetricSubject("orders.*"))
	po.Header = caller

	h := po.Headers()
	h.Set("Event-Type", "mutated")
	if got := caller.Get("Event-Type"); got != "order.created" {
		t.Fatalf("caller header mutated: %q", got)
	}
	if got := po.Headers().Get(jetstream.MsgIDHeader); got != "id-1" {
		t.Fatalf("%s = %q, want id-1", jetstream.MsgIDHeader, got)
	}
	if opts := po.jetStreamOpts(); len(opts) != 1 {
		t.Fatalf("jetStreamOpts = %v, want one WithMsgID", opts)
	}
	if opts := NewPublishSpec().jetStreamOpts(); opts != nil {
		t.Fatalf("jetStreamOpts without MsgID = %v, want nil", opts)
	}
}

func TestNatsHeaderCarrier(t *testing.T) {
	c := natsHeaderCarrier(nats.Header{})
	if got := c.Get("traceparent"); got != "" {
		t.Fatalf("Get on empty carrier = %q", got)
	}
	c.Set("traceparent", "00-x-y-01")
	if got := c.Get("traceparent"); got != "00-x-y-01" {
		t.Fatalf("Get = %q", got)
	}
	if keys := c.Keys(); len(keys) != 1 || keys[0] != "traceparent" {
		t.Fatalf("Keys = %v", keys)
	}
}

// inflightGroup терпит add во время wait: транспорт доставляет сообщения и
// после начала drain.
func TestInflightGroup_AddDuringWait(t *testing.T) {
	var g inflightGroup
	g.add()

	waited := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		waited <- g.wait(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	g.add()
	if got := g.count(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	g.done()
	g.done()

	if err := <-waited; err != nil {
		t.Fatalf("wait = %v, want nil", err)
	}
	if got := g.count(); got != 0 {
		t.Fatalf("count after done = %d", got)
	}
}

func TestInflightGroup_WaitHonoursContext(t *testing.T) {
	var g inflightGroup
	g.add()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := g.wait(ctx); err == nil {
		t.Fatal("wait with a busy handler = nil, want ctx error")
	}
	g.done()
	if err := g.wait(context.Background()); err != nil {
		t.Fatalf("wait on idle group = %v", err)
	}
}

func TestNakDelay_GrowsToCap(t *testing.T) {
	defaults := JetStreamConfig{NakBackoff: time.Second, NakMaxBackoff: time.Minute}
	tests := []struct {
		name string
		cfg  JetStreamConfig
		n    uint64
		want time.Duration
	}{
		{"first delivery", defaults, 1, time.Second},
		{"doubles per delivery", defaults, 3, 4 * time.Second},
		{"last below the cap", defaults, 6, 32 * time.Second},
		{"capped", defaults, 7, time.Minute},
		{"far past the cap", defaults, 1 << 40, time.Minute},
		{"own cap", JetStreamConfig{NakBackoff: 100 * time.Millisecond, NakMaxBackoff: 300 * time.Millisecond}, 4, 300 * time.Millisecond},
		{"zero backoff is an immediate nak", JetStreamConfig{}, 5, 0},
		{"cap below backoff keeps it constant", JetStreamConfig{NakBackoff: 2 * time.Minute, NakMaxBackoff: time.Minute}, 3, 2 * time.Minute},
		{"zero cap keeps the backoff constant", JetStreamConfig{NakBackoff: time.Second}, 4, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &jsBus{nakBackoff: tt.cfg.NakBackoff, nakMaxBackoff: nakMaxBackoff(tt.cfg)}
			if got := b.nakDelay(tt.n); got != tt.want {
				t.Fatalf("nakDelay(%d) = %v, want %v", tt.n, got, tt.want)
			}
		})
	}
}

// Дефолты держат сообщение 15 минут сбоя зависимости обработчика (failover Postgres,
// выкатка соседа): сумма задержек NAK до последней доставки — то, что обещает дока.
func TestNakDelay_DefaultBudgetCoversFifteenMinutes(t *testing.T) {
	for _, env := range []string{"NATS_JS_MAX_DELIVER", "NATS_JS_NAK_BACKOFF", "NATS_JS_NAK_MAX_BACKOFF"} {
		t.Setenv(env, "")
	}
	cfg, err := NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	b := &jsBus{nakBackoff: cfg.JetStream.NakBackoff, nakMaxBackoff: nakMaxBackoff(cfg.JetStream)}
	var budget time.Duration
	for n := uint64(1); n < uint64(cfg.JetStream.MaxDeliver); n++ {
		budget += b.nakDelay(n)
	}
	if budget < 15*time.Minute {
		t.Fatalf("default retry budget = %v over %d deliveries, want >= 15m", budget, cfg.JetStream.MaxDeliver)
	}
}

func TestCloseBudget(t *testing.T) {
	c := newConn(nil, nil, Config{DrainTimeout: time.Second})
	if got := c.closeBudget(context.Background()); got != time.Second {
		t.Fatalf("budget without deadline = %v, want DrainTimeout", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if got := c.closeBudget(ctx); got > 100*time.Millisecond {
		t.Fatalf("budget with a shorter deadline = %v", got)
	}

	expired, cancelExpired := context.WithTimeout(context.Background(), -time.Second)
	defer cancelExpired()
	if got := c.closeBudget(expired); got != 0 {
		t.Fatalf("budget with an expired ctx = %v, want 0", got)
	}
}

func TestSettledResult(t *testing.T) {
	pending := context.Canceled
	if got := settledResult(fakeFuture{ok: true}, pending); got != nil {
		t.Fatalf("settledResult(ok) = %v, want nil", got)
	}
	if got := settledResult(fakeFuture{err: errBoom}, pending); !errors.Is(got, errBoom) {
		t.Fatalf("settledResult(err) = %v, want errBoom", got)
	}
	if got := settledResult(fakeFuture{}, pending); !errors.Is(got, pending) {
		t.Fatalf("settledResult(pending) = %v, want %v", got, pending)
	}
}

var errBoom = errors.New("boom")

// fakeFuture — PubAckFuture с заранее известным исходом.
type fakeFuture struct {
	ok  bool
	err error
}

func (f fakeFuture) Ok() <-chan *jetstream.PubAck {
	ch := make(chan *jetstream.PubAck, 1)
	if f.ok {
		ch <- &jetstream.PubAck{}
	}
	return ch
}

func (f fakeFuture) Err() <-chan error {
	ch := make(chan error, 1)
	if f.err != nil {
		ch <- f.err
	}
	return ch
}

func (f fakeFuture) Msg() *nats.Msg { return nil }

// captureLogger — logkit.Logger, запоминающий последнюю запись.
type captureLogger struct {
	level string
	msg   string
	kv    []any
}

func (l *captureLogger) Debug(_ context.Context, msg string, kv ...any) { l.set("debug", msg, kv) }
func (l *captureLogger) Info(_ context.Context, msg string, kv ...any)  { l.set("info", msg, kv) }
func (l *captureLogger) Warn(_ context.Context, msg string, kv ...any)  { l.set("warn", msg, kv) }
func (l *captureLogger) Error(_ context.Context, msg string, kv ...any) { l.set("error", msg, kv) }
func (l *captureLogger) With(...any) logkit.Logger                      { return l }

func (l *captureLogger) Enabled(context.Context, slog.Level) bool { return true }

func (l *captureLogger) set(level, msg string, kv []any) {
	l.level, l.msg, l.kv = level, msg, kv
}

// Асинхронные ошибки клиента (slow consumer, сбой протокола) не теряются.
func TestConn_OnAsyncErrorLogs(t *testing.T) {
	log := &captureLogger{}
	c := newConn(nil, log, Config{Instance: "events"})

	c.onAsyncError(nil, nil, errors.New("slow consumer"))
	if log.level != "error" || log.msg != "natskit: async error" {
		t.Fatalf("async error logged as %s %q", log.level, log.msg)
	}
	if got := fmt.Sprint(log.kv...); !strings.Contains(got, "slow consumer") {
		t.Fatalf("async error kv = %v", log.kv)
	}
}

// Разрыв соединения во время Close — штатная часть shutdown, а не предупреждение.
func TestConn_OnDisconnectIsSilentWhileClosing(t *testing.T) {
	log := &captureLogger{}
	c := newConn(nil, log, Config{})

	c.onDisconnect(nil, errors.New("boom"))
	if log.msg != "natskit: disconnected" {
		t.Fatalf("disconnect not logged: %q", log.msg)
	}

	log.set("", "", nil)
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	c.onDisconnect(nil, errors.New("boom"))
	if log.msg != "" {
		t.Fatalf("disconnect logged while closing: %q", log.msg)
	}

	c.onClosed(nil)
	if log.msg != "" {
		t.Fatalf("expected close during Close to stay silent, got %q", log.msg)
	}
}

// stubMsg — jetstream.Msg с управляемым InProgress; остальные методы keepalive
// не вызывает, поэтому встроенный интерфейс остаётся нулевым.
type stubMsg struct {
	jetstream.Msg

	mu    sync.Mutex
	calls int
	err   error
}

func (m *stubMsg) InProgress() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.err
}

func (m *stubMsg) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// countingLogger считает записи по уровню и сообщению: тест keepalive проверяет,
// что предупреждение пишется один раз, а не на каждый тик.
type countingLogger struct {
	mu sync.Mutex
	n  map[string]int
}

func (l *countingLogger) add(level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n == nil {
		l.n = map[string]int{}
	}
	l.n[level+" "+msg]++
}

func (l *countingLogger) count(level, msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n[level+" "+msg]
}

func (l *countingLogger) Debug(_ context.Context, msg string, _ ...any) { l.add("debug", msg) }
func (l *countingLogger) Info(_ context.Context, msg string, _ ...any)  { l.add("info", msg) }
func (l *countingLogger) Warn(_ context.Context, msg string, _ ...any)  { l.add("warn", msg) }
func (l *countingLogger) Error(_ context.Context, msg string, _ ...any) { l.add("error", msg) }
func (l *countingLogger) With(...any) logkit.Logger                     { return l }

func (l *countingLogger) Enabled(context.Context, slog.Level) bool { return true }

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition %q not met in %s", what, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Транзиентная ошибка +WPI не выключает keepalive: иначе сервер переотправил бы
// сообщение живому обработчику. Предупреждение при этом пишется один раз.
func TestKeepInProgress_KeepsTickingAfterError(t *testing.T) {
	log := &countingLogger{}
	b := &jsBus{log: log, ackWait: 2 * minInProgressInterval}
	msg := &stubMsg{err: errBoom}

	stop := b.keepInProgress(context.Background(), msg, "orders.created")
	defer stop()

	waitFor(t, 5*time.Second, "three +WPI attempts", func() bool { return msg.count() >= 3 })
	stop()
	if got := log.count("warn", "natskit: jetstream in-progress"); got != 1 {
		t.Fatalf("in-progress warnings = %d, want exactly 1", got)
	}
}

// Подтверждённое сообщение выключает keepalive: продлевать нечего.
func TestKeepInProgress_StopsOnAckedMessage(t *testing.T) {
	log := &countingLogger{}
	b := &jsBus{log: log, ackWait: 2 * minInProgressInterval}
	msg := &stubMsg{err: jetstream.ErrMsgAlreadyAckd}

	stop := b.keepInProgress(context.Background(), msg, "orders.created")
	defer stop()

	waitFor(t, 5*time.Second, "first +WPI attempt", func() bool { return msg.count() >= 1 })
	// Негативное окно: за несколько тиков новых попыток быть не должно.
	time.Sleep(5 * minInProgressInterval)
	if got := msg.count(); got != 1 {
		t.Fatalf("+WPI attempts after ErrMsgAlreadyAckd = %d, want 1", got)
	}
	if got := log.count("warn", "natskit: jetstream in-progress"); got != 0 {
		t.Fatalf("acked message logged %d warnings, want 0", got)
	}
}

// Отмена ctx обработчика останавливает keepalive вместе с ним.
func TestKeepInProgress_StopsWithContext(t *testing.T) {
	b := &jsBus{log: &countingLogger{}, ackWait: 2 * minInProgressInterval}
	msg := &stubMsg{}
	ctx, cancel := context.WithCancel(context.Background())

	stop := b.keepInProgress(ctx, msg, "orders.created")
	defer stop()

	waitFor(t, 5*time.Second, "first +WPI attempt", func() bool { return msg.count() >= 1 })
	cancel()
	settled := msg.count()
	time.Sleep(5 * minInProgressInterval)
	if got := msg.count(); got > settled+1 {
		t.Fatalf("+WPI kept ticking after ctx cancel: %d → %d", settled, got)
	}
}
