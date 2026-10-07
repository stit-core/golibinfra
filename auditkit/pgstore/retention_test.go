package pgstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

func TestRetentionConfig_Validate(t *testing.T) {
	t.Parallel()
	for days, wantErr := range map[int]bool{
		0: true, -1: true, 1: false, 30: false,
		pgstore.MaxRetentionDays: false, pgstore.MaxRetentionDays + 1: true,
	} {
		err := pgstore.RetentionConfig{Days: days}.Validate()
		if (err != nil) != wantErr || (err != nil && !errors.Is(err, auditkit.ErrConfig)) {
			t.Errorf("Days %d: Validate() = %v, want ErrConfig %v", days, err, wantErr)
		}
	}
}

// Срок хранения аудита дефолта не имеет: без AUDIT_RETENTION_DAYS конфиг retention — ошибка.
func TestNewRetentionConfig(t *testing.T) {
	t.Setenv("AUDIT_RETENTION_DAYS", "")
	if _, err := pgstore.NewRetentionConfig(); !errors.Is(err, auditkit.ErrConfig) {
		t.Fatalf("NewRetentionConfig without AUDIT_RETENTION_DAYS = %v, want ErrConfig", err)
	}
	t.Setenv("AUDIT_RETENTION_DAYS", "90")
	cfg, err := pgstore.NewRetentionConfig()
	if err != nil || cfg.Days != 90 {
		t.Fatalf("NewRetentionConfig = %+v, %v; want Days 90", cfg, err)
	}
	t.Setenv("AUDIT_RETENTION_DAYS", "90d")
	if _, err := pgstore.NewRetentionConfig(); !errors.Is(err, auditkit.ErrConfig) {
		t.Fatalf("NewRetentionConfig(90d) = %v, want ErrConfig", err)
	}
}

// Невалидный срок до базы не доходит (у Store нет пула), job с ним не собирается.
func TestDeleteExpired_InvalidConfig(t *testing.T) {
	t.Parallel()
	s := pgstore.NewStore(nil)
	if err := s.DeleteExpired(context.Background(), pgstore.RetentionConfig{}); !errors.Is(err, auditkit.ErrConfig) {
		t.Fatalf("DeleteExpired(zero config) = %v, want ErrConfig", err)
	}
	if _, err := pgstore.RetentionJob(s, pgstore.RetentionConfig{}); !errors.Is(err, auditkit.ErrConfig) {
		t.Fatalf("RetentionJob(zero config) = %v, want ErrConfig", err)
	}
}

// Retention удаляет только записи старше срока по часам базы, батчами до конца отставания.
func TestStore_DeleteExpired(t *testing.T) {
	before := metrictest.Collect(t)
	s, pg, ctx := startStore(t)
	db := pg.DB(t)

	if _, err := db.Exec(ctx, `
		INSERT INTO audit_log (action, resource, at)
		SELECT 'old', 'user', NOW() - interval '31 days' FROM generate_series(1, $1)`, pgstore.RetentionBatchSize+1); err != nil {
		t.Fatalf("seed old rows: %v", err)
	}
	for _, e := range []auditkit.Entry{
		{Action: "edge", Resource: "user", At: time.Now().Add(-29 * 24 * time.Hour)},
		{Action: "fresh", Resource: "user"},
	} {
		if err := s.Log(ctx, e); err != nil {
			t.Fatalf("Log %s: %v", e.Action, err)
		}
	}

	job, err := pgstore.RetentionJob(s, pgstore.RetentionConfig{Days: 30})
	if err != nil {
		t.Fatalf("RetentionJob: %v", err)
	}
	if job.Name != "auditkit.retention" || job.Interval != time.Hour || job.Timeout != time.Hour {
		t.Fatalf("job = %s every %s (timeout %s), want auditkit.retention hourly", job.Name, job.Interval, job.Timeout)
	}
	if err := job.Run(ctx); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	left := map[string]int{}
	rows, err := db.Query(ctx, `SELECT action, count(*) FROM audit_log GROUP BY action`)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}
	for rows.Next() {
		var action string
		var n int
		if err := rows.Scan(&action, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		left[action] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if left["old"] != 0 || left["edge"] != 1 || left["fresh"] != 1 {
		t.Fatalf("rows after retention = %v, want only edge and fresh", left)
	}
	want := float64(pgstore.RetentionBatchSize + 1)
	if got := metrictest.Collect(t).Sub(before).Get(t, "audit_log_retention_deleted_total"); got.Kind != metrictest.Counter || got.Value != want {
		t.Fatalf("audit_log_retention_deleted_total = %v (%s), want counter delta %v", got, got.Kind, want)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.DeleteExpired(canceled, pgstore.RetentionConfig{Days: 30}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteExpired with canceled ctx = %v, want context.Canceled", err)
	}
}

func jobsOf(t *testing.T, opts ...fx.Option) ([]timerkit.Job, error) {
	t.Helper()
	var jobs []timerkit.Job
	app := fx.New(append([]fx.Option{
		fx.NopLogger,
		fx.Supply(&pgkit.DB{}),
		fx.Provide(logkit.Nop),
		pgstore.Module,
		timerkit.Module,
		fx.Populate(fx.Annotate(&jobs, fx.ParamTags(`group:"`+timerkit.JobsGroup+`"`))),
	}, opts...)...)
	return jobs, app.Err()
}

// Без RetentionModule аудит не удаляется; с ним — один job, невалидный срок или граф без
// timerkit.Module — ошибка сборки, а не job, который молча не идёт.
func TestRetentionModule(t *testing.T) {
	t.Parallel()
	jobs, err := jobsOf(t)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("Module alone: jobs %d, err %v; want no retention job", len(jobs), err)
	}
	jobs, err = jobsOf(t, pgstore.RetentionModule, fx.Supply(pgstore.RetentionConfig{Days: 30}))
	if err != nil || len(jobs) != 1 || jobs[0].Name != "auditkit.retention" {
		t.Fatalf("with RetentionModule: jobs %+v, err %v; want auditkit.retention", jobs, err)
	}
	if _, err := jobsOf(t, pgstore.RetentionModule, fx.Supply(pgstore.RetentionConfig{})); !errors.Is(err, auditkit.ErrConfig) {
		t.Fatalf("RetentionModule with zero config = %v, want ErrConfig", err)
	}
	err = fx.ValidateApp(
		fx.NopLogger,
		fx.Supply(&pgkit.DB{}, pgstore.RetentionConfig{Days: 30}),
		pgstore.Module,
		pgstore.RetentionModule,
	)
	if err == nil {
		t.Fatal("RetentionModule without timerkit.Module must fail the graph")
	}
}
