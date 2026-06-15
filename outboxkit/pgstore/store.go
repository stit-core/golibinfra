package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// DefaultTable — таблица single-instance Module (embedded-миграция 0001).
const DefaultTable = "outbox"

// tableNameRe ограничивает имя таблицы безопасным идентификатором: оно
// инлайнится в SQL (имя объекта нельзя передать плейсхолдером), поэтому
// валидируется на входе.
var tableNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type Store struct {
	db        *pgkit.DB
	table     string
	requireTx bool

	insertSQL  string
	listSQL    string
	markSQL    string
	failSQL    string
	backlogSQL string
}

type StoreOption func(*Store)

// WithRequireTx требует, чтобы Insert вызывался внутри транзакции (pgkit
// Transactor.WithTx) — иначе ошибка. Защищает инвариант outbox: событие
// пишется атомарно с бизнес-стейтом, а не отдельным коннектом из пула.
func WithRequireTx() StoreOption {
	return func(s *Store) { s.requireTx = true }
}

// NewStore строит Store на таблицу DefaultTable (single-instance путь, fx Module).
func NewStore(db *pgkit.DB, opts ...StoreOption) *Store {
	s, _ := newStore(db, DefaultTable, opts...) // DefaultTable валиден по построению
	return s
}

// NewStoreFor строит Store на произвольную таблицу — для нескольких независимых
// outbox-инстансов в одном приложении (таблица на инстанс). Имя валидируется;
// схему/партиции под кастомную таблицу заводит caller (SchemaSQL/PartitionSQL).
func NewStoreFor(db *pgkit.DB, table string, opts ...StoreOption) (*Store, error) {
	return newStore(db, table, opts...)
}

func newStore(db *pgkit.DB, table string, opts ...StoreOption) (*Store, error) {
	if !tableNameRe.MatchString(table) {
		return nil, fmt.Errorf("outboxkit: invalid table name %q", table)
	}
	s := &Store{
		db:        db,
		table:     table,
		insertSQL: fmt.Sprintf(`INSERT INTO %s (subject, payload, created_at) VALUES ($1, $2, NOW())`, table),
		listSQL:   fmt.Sprintf(`SELECT id, subject, payload, attempts FROM %s WHERE published_at IS NULL AND dead_at IS NULL ORDER BY created_at, id LIMIT $1 FOR UPDATE SKIP LOCKED`, table),
		markSQL:   fmt.Sprintf(`UPDATE %s SET published_at = $1 WHERE id = ANY($2)`, table),
		failSQL: fmt.Sprintf(`UPDATE %s o SET
    attempts = o.attempts + 1,
    last_error = f.err,
    dead_at = CASE WHEN f.dead THEN NOW() ELSE o.dead_at END
FROM unnest($1::bigint[], $2::text[], $3::bool[]) AS f(id, err, dead)
WHERE o.id = f.id`, table),
		backlogSQL: fmt.Sprintf(`SELECT subject,
    EXTRACT(EPOCH FROM (NOW() - MIN(created_at)))::float8 AS lag_seconds,
    COUNT(*) AS pending
FROM %s
WHERE published_at IS NULL AND dead_at IS NULL
GROUP BY subject`, table),
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

func (s *Store) Insert(ctx context.Context, subject string, payload any) error {
	if s.requireTx && !pgkit.InTx(ctx) {
		return fmt.Errorf("outboxkit: Insert требует активной транзакции (WithRequireTx) — оберни в pgkit Transactor.WithTx")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outboxkit: marshal: %w", err)
	}
	if _, err := s.db.Exec(ctx, s.insertSQL, subject, data); err != nil {
		return fmt.Errorf("outboxkit: insert: %w", err)
	}
	return nil
}

func (s *Store) ListUnpublished(ctx context.Context, limit int) ([]outboxkit.Message, error) {
	rows, err := s.db.Query(ctx, s.listSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("outboxkit: list: %w", err)
	}
	defer rows.Close()
	var msgs []outboxkit.Message
	for rows.Next() {
		var m outboxkit.Message
		if err := rows.Scan(&m.ID, &m.Subject, &m.Payload, &m.Attempts); err != nil {
			return nil, fmt.Errorf("outboxkit: scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outboxkit: rows: %w", err)
	}
	return msgs, nil
}

func (s *Store) MarkPublished(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := s.db.Exec(ctx, s.markSQL, time.Now(), ids); err != nil {
		return fmt.Errorf("outboxkit: mark published: %w", err)
	}
	return nil
}

// RecordFailures инкрементит attempts/last_error для неудачных публикаций и
// помечает dead_at у тех, кто исчерпал MaxAttempts (Failure.Dead). Одним
// UPDATE через unnest. Мёртвые строки далее не вычитываются ListUnpublished.
func (s *Store) RecordFailures(ctx context.Context, failures []outboxkit.Failure) error {
	if len(failures) == 0 {
		return nil
	}
	ids := make([]int64, len(failures))
	errs := make([]string, len(failures))
	dead := make([]bool, len(failures))
	for i, f := range failures {
		ids[i] = f.ID
		errs[i] = f.Err
		dead[i] = f.Dead
	}
	if _, err := s.db.Exec(ctx, s.failSQL, ids, errs, dead); err != nil {
		return fmt.Errorf("outboxkit: record failures: %w", err)
	}
	return nil
}

// Backlog реализует outboxkit.BacklogReporter — возраст самого старого и
// количество неопубликованных (не-dead) событий по каждому subject'у.
// Питает `outbox_lag_seconds`/`outbox_pending_count` через RegisterBacklogGauges.
func (s *Store) Backlog(ctx context.Context) ([]outboxkit.SubjectBacklog, error) {
	rows, err := s.db.Query(ctx, s.backlogSQL)
	if err != nil {
		return nil, fmt.Errorf("outboxkit: backlog query: %w", err)
	}
	defer rows.Close()
	var out []outboxkit.SubjectBacklog
	for rows.Next() {
		var b outboxkit.SubjectBacklog
		var seconds float64
		if err := rows.Scan(&b.Subject, &seconds, &b.Pending); err != nil {
			return nil, fmt.Errorf("outboxkit: backlog scan: %w", err)
		}
		b.Oldest = time.Duration(seconds * float64(time.Second))
		out = append(out, b)
	}
	return out, rows.Err()
}
