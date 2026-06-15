package pgstore

import (
	"context"
	"fmt"
	"time"
)

// SchemaSQL возвращает DDL партиционированной таблицы outbox + индекс для
// заданного имени — для bootstrap кастомных таблиц multi-instance (caller
// заводит свою миграцию). DefaultTable создаётся embedded-миграцией 0001.
func SchemaSQL(table string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s (
    id           BIGSERIAL    NOT NULL,
    subject      TEXT         NOT NULL,
    payload      JSONB        NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX IF NOT EXISTS %[1]s_unpublished_idx
    ON %[1]s (created_at, id) WHERE published_at IS NULL;`, table)
}

func PartitionSQL(table string, month time.Time) (name, sql string) {
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	name = fmt.Sprintf("%s_%04d_%02d", table, start.Year(), int(start.Month()))
	sql = fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
		name, table,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"),
	)
	return name, sql
}

// EnsurePartitions создаёт партиции outbox для текущего месяца и далее
// monthsAhead месяцев вперёд. Идемпотентна.
func (s *Store) EnsurePartitions(ctx context.Context, monthsAhead int) error {
	if monthsAhead < 0 {
		monthsAhead = 0
	}
	now := time.Now().UTC()
	for i := 0; i <= monthsAhead; i++ {
		m := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, i, 0)
		_, sql := PartitionSQL(s.table, m)
		if _, err := s.db.Exec(ctx, sql); err != nil {
			return fmt.Errorf("outboxkit: ensure partition %s: %w", m.Format("2006-01"), err)
		}
	}
	return nil
}
