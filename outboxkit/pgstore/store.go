// Package pgstore — Postgres-реализация outboxkit.Repository: партиционированная
// по месяцам таблица, тик воркера под FOR UPDATE, backlog и retention.
// Конфиг — `OUTBOX_RETENTION_MONTHS`, `OUTBOX_REQUIRE_TX`.
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// DefaultTable — таблица single-instance Module (embedded-миграции пакета).
const DefaultTable = "outbox"

// tableNameRe ограничивает имя таблицы безопасным идентификатором: оно
// инлайнится в SQL (имя объекта нельзя передать плейсхолдером), поэтому
// валидируется на входе.
var tableNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Store — outboxkit.Repository поверх одной outbox-таблицы.
type Store struct {
	db              *pgkit.DB
	table           string
	src             pgkit.Source
	requireTx       bool
	retentionMonths int
	clock           clockkit.Clock

	insertSQL     string
	listSkipSQL   string
	listHeadSQL   string
	markSQL       string
	failSQL       string
	reviveSQL     string
	backlogSQL    string
	pendingSQLFmt string
}

var (
	_ outboxkit.Repository      = (*Store)(nil)
	_ outboxkit.BacklogReporter = (*Store)(nil)
	_ outboxkit.DeadReviver     = (*Store)(nil)
	_ outboxkit.Maintainer      = (*Store)(nil)
	_ outboxkit.SchemaChecker   = (*Store)(nil)
)

// StoreOption — опция конструктора Store.
type StoreOption func(*Store)

// WithRequireTx требует, чтобы Insert вызывался внутри транзакции (pgkit
// Transactor.WithTx) — иначе outboxkit.ErrNoTx. Защищает инвариант outbox:
// событие пишется атомарно с бизнес-стейтом, а не отдельным коннектом из пула.
func WithRequireTx() StoreOption {
	return func(s *Store) { s.requireTx = true }
}

// WithRetention задаёт срок хранения партиций в месяцах для job'а retention
// (MaintenanceJobs); 0 — не дропать.
func WithRetention(months int) StoreOption {
	return func(s *Store) { s.retentionMonths = months }
}

// WithClock подставляет часы для календарных вычислений (партиции, retention).
func WithClock(c clockkit.Clock) StoreOption {
	return func(s *Store) {
		if c != nil {
			s.clock = c
		}
	}
}

// NewStore строит Store на таблицу DefaultTable (single-instance путь, fx Module); схема —
// источник Migrations.
func NewStore(db *pgkit.DB, opts ...StoreOption) *Store {
	s, _ := newStore(db, DefaultTable, Migrations, opts...) // DefaultTable валиден по построению
	return s
}

// NewStoreFor строит Store на произвольную таблицу — для нескольких независимых
// outbox-инстансов в одном приложении (таблица на инстанс). Имя валидируется;
// схему кастомной таблицы накатывает caller (TableMigrations/SchemaSQL).
func NewStoreFor(db *pgkit.DB, table string, opts ...StoreOption) (*Store, error) {
	if !tableNameRe.MatchString(table) {
		return nil, fmt.Errorf("outboxkit/pgstore: invalid table name %q", table)
	}
	return newStore(db, table, TableMigrations(table), opts...)
}

func newStore(db *pgkit.DB, table string, src pgkit.Source, opts ...StoreOption) (*Store, error) {
	const cols = `id, created_at, subject, payload, attempts,
    (next_attempt_at IS NULL OR next_attempt_at <= NOW()) AS due`
	s := &Store{
		db:        db,
		table:     table,
		src:       src,
		clock:     clockkit.Real{},
		insertSQL: fmt.Sprintf(`INSERT INTO %s (subject, payload, created_at) VALUES ($1, $2, NOW())`, table),
		// batch пропускает строки, ждущие backoff; ordered их не перескакивает
		// (порядок важнее пропускной способности) и отсекает хвост в коде.
		listSkipSQL: fmt.Sprintf(`SELECT %s FROM %s
WHERE published_at IS NULL AND dead_at IS NULL
    AND (next_attempt_at IS NULL OR next_attempt_at <= NOW())
ORDER BY created_at, id LIMIT $1 FOR UPDATE SKIP LOCKED`, cols, table),
		listHeadSQL: fmt.Sprintf(`SELECT %s FROM %s
WHERE published_at IS NULL AND dead_at IS NULL
ORDER BY created_at, id LIMIT $1 FOR UPDATE NOWAIT`, cols, table),
		// Диапазон created_at — ключ партиционирования: без него UPDATE идёт по
		// всем партициям таблицы.
		markSQL: fmt.Sprintf(`UPDATE %s SET published_at = NOW()
WHERE id = ANY($1) AND created_at >= $2 AND created_at <= $3`, table),
		failSQL: fmt.Sprintf(`UPDATE %s o SET
    attempts = o.attempts + 1,
    last_error = f.err,
    next_attempt_at = CASE WHEN f.retry_after > 0 THEN NOW() + make_interval(secs => f.retry_after) END,
    dead_at = CASE WHEN f.dead THEN NOW() ELSE o.dead_at END
FROM unnest($1::bigint[], $2::text[], $3::bool[], $4::float8[]) AS f(id, err, dead, retry_after)
WHERE o.id = f.id AND o.created_at >= $5 AND o.created_at <= $6`, table),
		reviveSQL: fmt.Sprintf(`UPDATE %s SET
    dead_at = NULL,
    attempts = 0,
    last_error = NULL,
    next_attempt_at = NULL
WHERE dead_at IS NOT NULL
    AND (cardinality($1::bigint[]) = 0 OR id = ANY($1))
    AND ($2::text = '' OR subject = $2)
    AND ($3::timestamptz IS NULL OR created_at >= $3)`, table),
		backlogSQL: fmt.Sprintf(`SELECT subject,
    EXTRACT(EPOCH FROM (NOW() - MIN(created_at)))::float8 AS lag_seconds,
    COUNT(*) AS pending
FROM %s
WHERE published_at IS NULL AND dead_at IS NULL
GROUP BY subject`, table),
		pendingSQLFmt: `SELECT EXISTS (SELECT 1 FROM %s WHERE published_at IS NULL AND dead_at IS NULL)`,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Table — имя outbox-таблицы этого Store.
func (s *Store) Table() string { return s.table }

// Insert пишет событие в outbox-таблицу; payload уже сериализован (outboxkit.Bus).
func (s *Store) Insert(ctx context.Context, subject string, payload json.RawMessage) error {
	if s.requireTx && !pgkit.InTx(ctx) {
		return fmt.Errorf("%w: wrap the call in pgkit Transactor.WithTx", outboxkit.ErrNoTx)
	}
	if len(payload) == 0 {
		return fmt.Errorf("outboxkit/pgstore: insert %q: empty payload", subject)
	}
	if _, err := s.db.Exec(ctx, s.insertSQL, subject, []byte(payload)); err != nil {
		return fmt.Errorf("outboxkit/pgstore: insert: %w", err)
	}
	return nil
}

// ListUnpublished запирает выбранные строки (FOR UPDATE) до конца транзакции
// вызывающего: batch — SKIP LOCKED, ordered — NOWAIT, где занятая голова очереди
// становится outboxkit.ErrBusy. Строки, ждущие backoff (next_attempt_at в
// будущем), в batch пропускаются, в ordered обрывают выборку.
func (s *Store) ListUnpublished(ctx context.Context, limit int, mode outboxkit.PublishMode) ([]outboxkit.Message, error) {
	q := s.listSkipSQL
	if mode == outboxkit.PublishOrdered {
		q = s.listHeadSQL
	}
	rows, err := s.db.Query(ctx, q, limit)
	if err != nil {
		return nil, listErr(err)
	}
	defer rows.Close()
	var msgs []outboxkit.Message
	for rows.Next() {
		var m outboxkit.Message
		var due bool
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.Subject, &m.Payload, &m.Attempts, &due); err != nil {
			return nil, fmt.Errorf("outboxkit/pgstore: scan: %w", err)
		}
		if !due {
			break
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, listErr(err)
	}
	return msgs, nil
}

func listErr(err error) error {
	if pgkit.IsLockNotAvailable(err) {
		return outboxkit.ErrBusy
	}
	return fmt.Errorf("outboxkit/pgstore: list: %w", err)
}

// MarkPublished проставляет published_at строкам батча; refs несут ключ
// партиционирования, поэтому UPDATE идёт только по затронутым партициям.
func (s *Store) MarkPublished(ctx context.Context, refs []outboxkit.MessageRef) error {
	if len(refs) == 0 {
		return nil
	}
	ids, from, to := refBounds(refs)
	if _, err := s.db.Exec(ctx, s.markSQL, ids, from, to); err != nil {
		return fmt.Errorf("outboxkit/pgstore: mark published: %w", err)
	}
	return nil
}

// RecordFailures инкрементит attempts/last_error, откладывает следующую попытку
// на Failure.RetryAfter и помечает dead_at у исчерпавших MaxAttempts — одним
// UPDATE через unnest. Мёртвые строки далее не вычитываются ListUnpublished.
func (s *Store) RecordFailures(ctx context.Context, failures []outboxkit.Failure) error {
	if len(failures) == 0 {
		return nil
	}
	refs := make([]outboxkit.MessageRef, len(failures))
	errs := make([]string, len(failures))
	dead := make([]bool, len(failures))
	retry := make([]float64, len(failures))
	for i, f := range failures {
		refs[i] = f.Ref
		errs[i] = f.Err
		dead[i] = f.Dead
		retry[i] = f.RetryAfter.Seconds()
	}
	ids, from, to := refBounds(refs)
	if _, err := s.db.Exec(ctx, s.failSQL, ids, errs, dead, retry, from, to); err != nil {
		return fmt.Errorf("outboxkit/pgstore: record failures: %w", err)
	}
	return nil
}

// ReviveDead возвращает dead-строки в очередь (attempts, last_error и backoff
// сбрасываются) и отдаёт число задетых строк.
func (s *Store) ReviveDead(ctx context.Context, filter outboxkit.ReviveFilter) (int64, error) {
	ids := filter.IDs
	if ids == nil {
		ids = []int64{}
	}
	var since *time.Time
	if !filter.Since.IsZero() {
		since = &filter.Since
	}
	tag, err := s.db.Exec(ctx, s.reviveSQL, ids, filter.Subject, since)
	if err != nil {
		return 0, fmt.Errorf("outboxkit/pgstore: revive dead: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Backlog реализует outboxkit.BacklogReporter — возраст самого старого и
// количество неопубликованных (не-dead) событий по каждому subject'у.
// Питает `outbox_lag_seconds`/`outbox_pending_count` через RegisterBacklogGauges.
func (s *Store) Backlog(ctx context.Context) ([]outboxkit.SubjectBacklog, error) {
	rows, err := s.db.Query(ctx, s.backlogSQL)
	if err != nil {
		return nil, fmt.Errorf("outboxkit/pgstore: backlog query: %w", err)
	}
	defer rows.Close()
	var out []outboxkit.SubjectBacklog
	for rows.Next() {
		var b outboxkit.SubjectBacklog
		var seconds float64
		if err := rows.Scan(&b.Subject, &seconds, &b.Pending); err != nil {
			return nil, fmt.Errorf("outboxkit/pgstore: backlog scan: %w", err)
		}
		b.Oldest = time.Duration(seconds * float64(time.Second))
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outboxkit/pgstore: backlog: %w", err)
	}
	return out, nil
}

// MaintenanceJobs — партиции (раз в сутки и на старте) и retention, если он
// включён (WithRetention). Instance кладёт их в fx-группу timerkit.JobsGroup.
func (s *Store) MaintenanceJobs() []timerkit.Job {
	jobs := []timerkit.Job{PartitionKeeperJob(s)}
	if s.retentionMonths > 0 {
		jobs = append(jobs, RetentionKeeperJob(s, s.retentionMonths))
	}
	return jobs
}

// refBounds — id'шники батча и границы ключа партиционирования по нему.
func refBounds(refs []outboxkit.MessageRef) (ids []int64, from, to time.Time) {
	ids = make([]int64, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
		if i == 0 || r.CreatedAt.Before(from) {
			from = r.CreatedAt
		}
		if i == 0 || r.CreatedAt.After(to) {
			to = r.CreatedAt
		}
	}
	return ids, from, to
}
