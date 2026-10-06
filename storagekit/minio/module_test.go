package minio_test

import (
	"strings"
	"testing"
	"time"

	miniogo "github.com/minio/minio-go/v7"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
	skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
)

func appOptions(cfg storagekit.Config) fx.Option {
	return fx.Options(
		fx.NopLogger,
		fx.Supply(cfg),
		fx.Provide(logkit.Nop),
		storagekit.Module,
		skminio.Module,
	)
}

func TestModule_GraphIsValid(t *testing.T) {
	cfg := unitConfig()
	cfg.ProbeOnStart = false

	var (
		storage   storagekit.Storage
		presigner storagekit.Presigner
	)
	if err := fx.ValidateApp(appOptions(cfg), fx.Populate(&storage, &presigner)); err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}

	// Порты отдаются тем же *Storage: сборка графа не должна давать две реализации.
	app := fx.New(appOptions(cfg), fx.Populate(&storage, &presigner))
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	if storage == nil || presigner == nil {
		t.Fatalf("Module must provide Storage and Presigner, got %v / %v", storage, presigner)
	}
	if any(storage) != any(presigner) {
		t.Fatal("Storage and Presigner must be the same adapter")
	}
}

// Ошибка проверки бакета на старте видна как ошибка старта, а не как «сервис
// поднялся, но пишет в никуда».
func TestModule_BucketCheckFailureFailsStart(t *testing.T) {
	cfg := unitConfig()
	cfg.Endpoint = "192.0.2.1:9000"
	cfg.MaxRetries = 0
	cfg.CreateBucket = true
	cfg.ProbeTimeout = 300 * time.Millisecond

	app := fx.New(appOptions(cfg))
	err := app.Start(t.Context())
	if err == nil {
		_ = app.Stop(t.Context())
		t.Fatal("start against an unreachable endpoint = nil, want an error")
	}
	if !strings.Contains(err.Error(), "bucket exists check") {
		t.Fatalf("start error = %v, want it to name the bucket check", err)
	}
}

func TestProbe_LandsInHealthGroup(t *testing.T) {
	cfg := unitConfig()
	cfg.ProbeOnStart = false

	type in struct {
		fx.In
		Probes []healthkit.Probe `group:"healthkit.probes"`
	}
	var probes []healthkit.Probe
	app := fx.New(
		appOptions(cfg),
		healthkit.ProvideProbe(skminio.Probe),
		fx.Invoke(func(i in) { probes = i.Probes }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	if len(probes) != 1 || probes[0].Name != skminio.ProbeName {
		t.Fatalf("probes in health group = %+v, want one named %q", probes, skminio.ProbeName)
	}
}

func TestModule_ProbesBucketOnStart(t *testing.T) {
	cfg := containerConfig(t, func(c *storagekit.Config) { c.ProbeOnStart = true })

	app := fx.New(appOptions(cfg))
	err := app.Start(t.Context())
	if err == nil {
		_ = app.Stop(t.Context())
		t.Fatal("start with a missing bucket must fail")
	}
	if !strings.Contains(err.Error(), cfg.Bucket) {
		t.Fatalf("start error = %v, want it to name bucket %q", err, cfg.Bucket)
	}

	client, cerr := skminio.NewClient(cfg)
	if cerr != nil {
		t.Fatalf("NewClient: %v", cerr)
	}
	if merr := client.MakeBucket(t.Context(), cfg.Bucket, miniogo.MakeBucketOptions{}); merr != nil {
		t.Fatalf("make bucket: %v", merr)
	}

	ok := fx.New(appOptions(cfg))
	if serr := ok.Start(t.Context()); serr != nil {
		t.Fatalf("start with an existing bucket: %v", serr)
	}
	if serr := ok.Stop(t.Context()); serr != nil {
		t.Fatalf("stop: %v", serr)
	}
}

func TestModule_CreatesBucketOnStart(t *testing.T) {
	cfg := containerConfig(t, func(c *storagekit.Config) {
		c.CreateBucket = true
		// Создание бакета не требует включённой стартовой пробы.
		c.ProbeOnStart = false
	})

	app := fx.New(appOptions(cfg))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start with CreateBucket: %v", err)
	}
	defer func() {
		if err := app.Stop(t.Context()); err != nil {
			t.Fatalf("stop: %v", err)
		}
	}()

	client, err := skminio.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	exists, err := client.BucketExists(t.Context(), cfg.Bucket)
	if err != nil || !exists {
		t.Fatalf("bucket %q exists = %v, err = %v", cfg.Bucket, exists, err)
	}

	// Рестарт с тем же бакетом — обычный случай: существующий бакет не ошибка.
	cfg.ProbeOnStart = true
	again := fx.New(appOptions(cfg))
	if err := again.Start(t.Context()); err != nil {
		t.Fatalf("start with an existing bucket and CreateBucket=true: %v", err)
	}
	if err := again.Stop(t.Context()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestModule_SkipsStartupCheckWhenDisabled(t *testing.T) {
	cfg := unitConfig()
	cfg.Endpoint = "192.0.2.1:9000"
	cfg.MaxRetries = 0
	cfg.ProbeOnStart = false
	cfg.ProbeTimeout = 500 * time.Millisecond

	app := fx.New(appOptions(cfg))
	start := time.Now()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start without ProbeOnStart must not touch the network: %v", err)
	}
	if err := app.Stop(t.Context()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("start took %s: startup hook is talking to S3 with ProbeOnStart=false", elapsed)
	}
}
