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
	db    *pgkit.DB
	table string

	insertSQL string
	listSQL   string
	markSQL   string
	lagSQL    string
}

// NewStore строит Store на таблицу DefaultTable (single-instance путь, fx Module).
func NewStore(db *pgkit.DB) *Store {
	s, _ := newStore(db, DefaultTable) // DefaultTable валиден по построению
	return s
}

// NewStoreFor строит Store на произвольную таблицу — для нескольких независимых
// outbox-инстансов в одном приложении (таблица на инстанс). Имя валидируется;
// схему/партиции под кастомную таблицу заводит caller (SchemaSQL/PartitionSQL).
func NewStoreFor(db *pgkit.DB, table string) (*Store, error) {
	return newStore(db, table)
}

func newStore(db *pgkit.DB, table string) (*Store, error) {
	if !tableNameRe.MatchString(table) {
		return nil, fmt.Errorf("outboxkit: invalid table name %q", table)
	}
	return &Store{
		db:        db,
		table:     table,
		insertSQL: fmt.Sprintf(`INSERT INTO %s (subject, payload, created_at) VALUES ($1, $2, NOW())`, table),
		listSQL:   fmt.Sprintf(`SELECT id, subject, payload FROM %s WHERE published_at IS NULL ORDER BY created_at, id LIMIT $1 FOR UPDATE SKIP LOCKED`, table),
		markSQL:   fmt.Sprintf(`UPDATE %s SET published_at = $1 WHERE id = ANY($2)`, table),
		lagSQL: fmt.Sprintf(`SELECT subject, EXTRACT(EPOCH FROM (NOW() - MIN(created_at)))::float8 AS lag_seconds
FROM %s
WHERE published_at IS NULL
GROUP BY subject`, table),
	}, nil
}

func (s *Store) Insert(ctx context.Context, subject string, payload any) error {
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
		if err := rows.Scan(&m.ID, &m.Subject, &m.Payload); err != nil {
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

// LagBySubject реализует outboxkit.LagReporter — возвращает возраст
// самого старого неопубликованного события для каждого subject'а.
// Используется outboxkit.RegisterLagGauge для экспонирования
// `outbox_lag_seconds{subject}` как ObservableGauge.
func (s *Store) LagBySubject(ctx context.Context) (map[string]time.Duration, error) {
	rows, err := s.db.Query(ctx, s.lagSQL)
	if err != nil {
		return nil, fmt.Errorf("outboxkit: lag query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]time.Duration)
	for rows.Next() {
		var subj string
		var seconds float64
		if err := rows.Scan(&subj, &seconds); err != nil {
			return nil, fmt.Errorf("outboxkit: lag scan: %w", err)
		}
		out[subj] = time.Duration(seconds * float64(time.Second))
	}
	return out, rows.Err()
}
