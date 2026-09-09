package minio_test

import (
	"context"
	"strings"
	"testing"
	"time"

	miniogo "github.com/minio/minio-go/v7"

	skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
)

func TestProbe_Name(t *testing.T) {
	cfg := unitConfig()
	client, err := skminio.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := skminio.Probe(client, cfg).Name; got != skminio.ProbeName {
		t.Fatalf("probe name = %q, want %q", got, skminio.ProbeName)
	}
}

func TestProbe_BucketState(t *testing.T) {
	cfg := containerConfig(t)
	client, err := skminio.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	err = skminio.Probe(client, cfg).Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), cfg.Bucket) {
		t.Fatalf("probe on a missing bucket = %v, want an error naming %q", err, cfg.Bucket)
	}

	if err := client.MakeBucket(t.Context(), cfg.Bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}
	if err := skminio.Probe(client, cfg).Run(t.Context()); err != nil {
		t.Fatalf("probe on an existing bucket = %v, want nil", err)
	}
}

// Регрессия F3: без собственного дедлайна проба уходила в полный retry-цикл
// minio-go (до 10 попыток с dial-таймаутом 30 s) и держала /ready.
func TestProbe_RespectsOwnTimeout(t *testing.T) {
	tests := map[string]string{
		"connection refused": "127.0.0.1:1",
		// TEST-NET-1: пакеты уходят в никуда, dial висит до таймаута.
		"black hole": "192.0.2.1:9000",
	}
	for name, endpoint := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := unitConfig()
			cfg.Endpoint = endpoint
			cfg.MaxRetries = 0
			cfg.ProbeTimeout = 500 * time.Millisecond

			client, err := skminio.NewClient(cfg)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			start := time.Now()
			err = skminio.Probe(client, cfg).Run(context.Background())
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("probe against an unreachable endpoint must fail")
			}
			if elapsed > 3*time.Second {
				t.Fatalf("probe took %s, want it bounded by ProbeTimeout %s", elapsed, cfg.ProbeTimeout)
			}
		})
	}
}

func TestProbe_ZeroTimeoutKeepsCallerDeadline(t *testing.T) {
	cfg := unitConfig()
	cfg.Endpoint = "192.0.2.1:9000"
	cfg.MaxRetries = 0
	cfg.ProbeTimeout = 0

	client, err := skminio.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := skminio.Probe(client, cfg).Run(ctx); err == nil {
		t.Fatal("probe against an unreachable endpoint must fail")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("probe took %s, want it bounded by the caller deadline", elapsed)
	}
}
