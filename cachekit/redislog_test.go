package cachekit

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

// useRedisLog — цель моста на время теста: мост у go-redis один на процесс.
func useRedisLog(t *testing.T) {
	t.Helper()
	prev := redisLog.target.Load()
	redisLog.target.Store(nil)
	t.Cleanup(func() { redisLog.target.Store(prev) })
}

// Регрессия KOBS-08: внутренний лог go-redis (отказ пула) — запись WARN логгера кэша, а не текст в
// stderr мимо logkit.
func TestRedisLog_GoRedisLinesGoToLogkit(t *testing.T) {
	useRedisLog(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	rec := logtest.Capture()
	c, err := newRedisCache(Config{
		URL: "redis://" + addr, PoolSize: 1, MaxRetries: -1,
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
	}, rec)
	if err != nil {
		t.Fatalf("newRedisCache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("Ping of a closed port = nil, want an error")
	}
	for _, e := range rec.Entries() {
		if e.Level == slog.LevelWarn && e.Msg == "redis" && strings.Contains(e.Str("detail"), "failed to dial") {
			return
		}
	}
	t.Fatalf("no WARN record of the go-redis dial failure: %+v", rec.Entries())
}

// Пока логгера у моста нет, строка go-redis не теряется: она идёт в stderr, как без моста.
func TestRedisLog_WithoutLoggerWritesToStderr(t *testing.T) {
	useRedisLog(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })

	redisLog.Printf(context.Background(), "pool: %s", "exhausted")
	os.Stderr = old
	_ = w.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if string(got) != "redis: pool: exhausted\n" {
		t.Fatalf("stderr = %q, want the go-redis line", got)
	}
}

// nil и Nop мост не занимают: кэш без логгера не глушит лог go-redis другого кэша процесса.
func TestRedisLog_NilAndNopKeepTarget(t *testing.T) {
	useRedisLog(t)
	rec := logtest.Capture()
	redisLog.install(rec)
	redisLog.install(nil)
	redisLog.install(logkit.Nop())
	redisLog.Printf(context.Background(), "redis: %s", "pool exhausted")
	if got := rec.Entries(); len(got) != 1 || got[0].Str("detail") != "redis: pool exhausted" {
		t.Fatalf("entries = %+v, want the record in the real logger", got)
	}
}
