package grpckit

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

func overallStatus(t *testing.T, s *Server) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	resp, err := s.health.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	return resp.GetStatus()
}

// Дедлайн пробы — GRPC_HEALTH_PROBE_TIMEOUT, а не период тика: зависшая
// зависимость видна раньше следующей переоценки, а не через N интервалов.
func TestHealthProbeTimeoutIsIndependentOfInterval(t *testing.T) {
	const (
		interval     = 400 * time.Millisecond
		probeTimeout = 50 * time.Millisecond
	)
	rec := logtest.Capture()
	srv, err := New(
		Config{HealthInterval: interval, HealthProbeTimeout: probeTimeout},
		rec,
		WithHealthProbes(healthkit.New("dep", func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.SetServing("", true)

	start := time.Now()
	srv.startHealthWatch()
	t.Cleanup(srv.stopHealthWatch)

	deadline := time.Now().Add(interval)
	for overallStatus(t, srv) != healthpb.HealthCheckResponse_NOT_SERVING {
		if time.Now().After(deadline) {
			t.Fatalf("a hung probe must flip overall to NOT_SERVING within %s (probe deadline %s)", interval, probeTimeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if elapsed := time.Since(start); elapsed >= interval {
		t.Fatalf("overall flipped after %s: the probe deadline is tied to GRPC_HEALTH_INTERVAL", elapsed)
	}

	e, ok := rec.Find(slog.LevelWarn, "grpckit: health not serving")
	if !ok || e.Str("probe") != "dep" || !strings.Contains(e.Str("error"), probeTimeout.String()) {
		t.Fatalf("log record must name the probe and its deadline, got %+v ok=%v", e, ok)
	}
}

// Serve на втором слушателе не поднимает второй health-loop, а после Stop
// наблюдение не возвращается: иначе остановленный сервер снова отвечал бы SERVING.
func TestStartHealthWatchRunsSingleLoop(t *testing.T) {
	evaluated := make(chan struct{}, 4)
	srv, err := New(
		Config{HealthInterval: 10 * time.Millisecond},
		nil,
		WithHealthProbes(healthkit.New("dep", func(context.Context) error {
			select {
			case evaluated <- struct{}{}:
			default:
			}
			return nil
		})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.SetServing("", true)

	srv.startHealthWatch()
	first := srv.healthDone
	if first == nil {
		t.Fatal("startHealthWatch did not start the loop")
	}
	srv.startHealthWatch()
	if srv.healthDone != first {
		t.Fatal("second startHealthWatch started a second loop")
	}

	// Второй тик означает, что переоценка со статусом «без изменений» дошла до конца.
	<-evaluated
	<-evaluated
	if got := overallStatus(t, srv); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("overall = %v, want SERVING while probes pass", got)
	}

	srv.stopHealthWatch()
	srv.startHealthWatch()
	if srv.healthDone != nil {
		t.Fatal("health watch restarted after stop")
	}
}

// Отмена наблюдения — не отказ зависимости: статус остаётся прежним, иначе
// каждый Stop писал бы в лог ложную деградацию.
func TestHealthWatchCancelKeepsStatus(t *testing.T) {
	var once sync.Once
	started := make(chan struct{})
	srv, err := New(
		Config{HealthInterval: time.Hour, HealthProbeTimeout: time.Hour},
		nil,
		WithHealthProbes(healthkit.New("slow", func(ctx context.Context) error {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return ctx.Err()
		})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.SetServing("", true)

	srv.startHealthWatch()
	<-started
	srv.stopHealthWatch()

	if got := overallStatus(t, srv); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("overall = %v, want SERVING", got)
	}
}
