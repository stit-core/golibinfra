package cachekit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestCheckInfoVersion(t *testing.T) {
	tests := []struct {
		name    string
		info    string
		wantErr bool
	}{
		{"redis 7", "# Server\r\nredis_version:7.2.4\r\nos:Linux\r\n", false},
		{"redis 8", "redis_version:8.0.0\n", false},
		{"valkey reporting compat version", "redis_version:7.2.4\nvalkey_version:8.0.1\n", false},
		{"redis 6", "# Server\r\nredis_version:6.2.24\r\n", true},
		{"redis 5", "redis_version:5.0.14\n", true},
		{"no version field", "# Server\r\nos:Linux\r\n", true},
		{"unparsable version", "redis_version:experimental\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkInfoVersion(tt.info)
			if tt.wantErr && !errors.Is(err, ErrServerVersion) {
				t.Fatalf("checkInfoVersion = %v, want ErrServerVersion", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("checkInfoVersion = %v, want nil", err)
			}
		})
	}
	if got := infoField("# Server\r\nredis_version:7.2.4\r\n", "redis_version"); got != "7.2.4" {
		t.Fatalf("infoField = %q", got)
	}
}

// failCmdHook отвергает команду по имени, не отправляя её на сервер.
type failCmdHook struct {
	cmd string
	err error
}

func (failCmdHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h failCmdHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == h.cmd {
			return h.err
		}
		return next(ctx, cmd)
	}
}

func (failCmdHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// Снятие членства и удаление ключей — одна серверная операция: отдельный UNLINK,
// упавший по сети, оставил бы ключи в Redis вне какого-либо тега, и повторная
// инвалидация их бы уже не нашла.
func TestRedisCache_InvalidateByTagIsOneCommand(t *testing.T) {
	url := testkit.StartRedis(t).URL
	c, err := newRedisCache(Config{
		URL: url, PoolSize: 5, MaxRetries: -1,
		DialTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("newRedisCache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.client.AddHook(failCmdHook{cmd: "unlink", err: errors.New("network is down")})

	ctx := context.Background()
	keys := []string{"a", "b", "c"}
	for _, k := range keys {
		if err := c.SetWithTags(ctx, k, []byte("v"), time.Minute, "t"); err != nil {
			t.Fatalf("set with tags: %v", err)
		}
	}

	if err := c.InvalidateByTag(ctx, "t"); err != nil {
		t.Fatalf("InvalidateByTag: %v", err)
	}
	for _, k := range keys {
		if _, found, err := c.Get(ctx, k); err != nil || found {
			t.Fatalf("key %q survived invalidation: found=%v err=%v", k, found, err)
		}
	}
	if n, err := c.client.Exists(ctx, "tag:t").Result(); err != nil || n != 0 {
		t.Fatalf("tag set after invalidation: exists=%d err=%v", n, err)
	}
}

// Недоступный сервер — ошибка проверки, а не молчаливое «версия неизвестна».
func TestCheckServerVersion_PropagatesClientError(t *testing.T) {
	c, err := newRedisCache(Config{
		URL: "redis://127.0.0.1:6379/0", PoolSize: 1, MaxRetries: -1,
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("newRedisCache: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.checkServerVersion(context.Background()); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("checkServerVersion on a closed client = %v, want a wrapped redis.ErrClosed", err)
	}
}

// busyScript крутит однопоточный Redis ARGV[1] итераций: команды других клиентов ждут в сокетах и
// выполняются после — как за долгим скриптом соседа или fork под BGSAVE. TIME внутри скрипта
// заморожен, поэтому длительность задаёт число итераций, а не часы.
const busyScript = `for i = 1, tonumber(ARGV[1]) do end return 1`

// busyIterations — число итераций busyScript на ~d у этого сервера: калибровка пробным прогоном.
// d — с запасом до busy-reply-threshold (5 с), после которого очередь получает BUSY.
func busyIterations(t *testing.T, raw *redis.Client, d time.Duration) int64 {
	t.Helper()
	const probe = 50_000_000
	start := time.Now()
	if err := raw.Eval(context.Background(), busyScript, nil, probe).Err(); err != nil {
		t.Fatalf("calibrate busy script: %v", err)
	}
	return int64(float64(probe) * float64(d) / float64(max(time.Since(start), time.Millisecond)))
}

// warmPool кладёт в пул клиента n готовых соединений: повтор на занятом сервере идёт по ним, а не
// упирается в рукопожатие нового соединения, которое ждёт тот же сервер.
func warmPool(t *testing.T, client *redis.Client, n int) {
	t.Helper()
	conns := make([]*redis.Conn, n)
	for i := range conns {
		conns[i] = client.Conn()
		if err := conns[i].Ping(context.Background()).Err(); err != nil {
			t.Fatalf("warm pool: %v", err)
		}
	}
	for _, cn := range conns {
		_ = cn.Close()
	}
}

// Регрессия KCOR-04 (E18): ответ занятого сервера не приходит за REDIS_READ_TIMEOUT, а команда уже
// в очереди. SetNX, GetDel, CompareAndDelete и CompareAndSet не повторяются — повтор увидел бы
// эффект первой попытки и ответил бы ложным «нет», а вызывающий получает ошибку; Get повторяется.
func TestRedisCache_NonIdempotentCommandsAreNotRetried(t *testing.T) {
	url := testkit.StartRedis(t).URL
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	raw := redis.NewClient(opts)
	t.Cleanup(func() { _ = raw.Close() })
	c, err := newRedisCache(Config{
		URL: url, PoolSize: 16, MaxRetries: 10,
		DialTimeout: 5 * time.Second, ReadTimeout: 200 * time.Millisecond, WriteTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("newRedisCache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	// Скрипты — в кэше сервера, как после первого вызова: иначе первая попытка — NOSCRIPT без эффекта.
	for _, s := range []*redis.Script{compareAndDeleteScript, compareAndSetScript} {
		if err := s.Load(ctx, raw).Err(); err != nil {
			t.Fatalf("script load: %v", err)
		}
	}
	iterations := busyIterations(t, raw, 500*time.Millisecond)
	busy := func() (wait func()) {
		warmPool(t, c.client, 12)
		warmPool(t, c.once, 2)
		done := make(chan error, 1)
		go func() { done <- raw.Eval(context.Background(), busyScript, nil, iterations).Err() }()
		time.Sleep(50 * time.Millisecond)
		return func() {
			if err := <-done; err != nil {
				t.Errorf("busy script: %v", err)
			}
		}
	}

	_ = c.Set(ctx, "otp", []byte("token"), time.Minute)
	wait := busy()
	_, found, err := c.GetDel(ctx, "otp")
	wait()
	if err == nil && !found {
		t.Error("GetDel on a busy server: found=false, err=nil — a retry saw the first attempt")
	}

	wait = busy()
	ok, err := c.SetNX(ctx, "lock", []byte("me"), time.Minute)
	wait()
	if holder := raw.Get(ctx, "lock").Val(); err == nil && !ok && holder == "me" {
		t.Error("SetNX of the only writer on a busy server: ok=false, err=nil — a retry saw its own value")
	}

	_ = c.Set(ctx, "cad", []byte("mine"), time.Minute)
	wait = busy()
	ok, err = c.CompareAndDelete(ctx, "cad", []byte("mine"))
	wait()
	if left := raw.Exists(ctx, "cad").Val(); err == nil && !ok && left == 0 {
		t.Error("CompareAndDelete on a busy server: ok=false, err=nil — a retry saw the first attempt")
	}

	_ = c.Set(ctx, "cas", []byte("mine"), time.Minute)
	wait = busy()
	ok, err = c.CompareAndSet(ctx, "cas", []byte("mine"), []byte("new"), time.Minute)
	wait()
	if v := raw.Get(ctx, "cas").Val(); err == nil && !ok && v == "new" {
		t.Error("CompareAndSet on a busy server: ok=false, err=nil — a retry saw the first attempt")
	}

	_ = c.Set(ctx, "plain", []byte("v"), time.Minute)
	wait = busy()
	v, found, err := c.Get(ctx, "plain")
	wait()
	if err != nil || !found || string(v) != "v" {
		t.Errorf("Get on a busy server: %q, %v, %v; want the value after a retry", v, found, err)
	}
}
