package pgstore

import (
	"context"
	"fmt"
	"strings"
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
    attempts     INT          NOT NULL DEFAULT 0,
    last_error   TEXT,
    dead_at      TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX IF NOT EXISTS %[1]s_unpublished_idx
    ON %[1]s (created_at, id) WHERE published_at IS NULL AND dead_at IS NULL;`, table)
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

const listPartitionsSQL = `SELECT c.relname FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_class p ON p.oid = i.inhparent
WHERE p.relname = $1`

// DropOldPartitions удаляет партиции, чей месяц старше keepMonths месяцев до
// текущего (retention). keepMonths <= 0 — no-op. DROP всей партиции дешевле, чем
// DELETE строк — ради этого outbox и партиционирован по месяцам.
func (s *Store) DropOldPartitions(ctx context.Context, keepMonths int) error {
	if keepMonths <= 0 {
		return nil
	}
	now := time.Now().UTC()
	cutoff := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -keepMonths, 0)

	rows, err := s.db.Query(ctx, listPartitionsSQL, s.table)
	if err != nil {
		return fmt.Errorf("outboxkit: list partitions: %w", err)
	}
	var drop []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("outboxkit: scan partition: %w", err)
		}
		if m, ok := partitionMonth(s.table, name); ok && m.Before(cutoff) {
			drop = append(drop, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("outboxkit: list partitions: %w", err)
	}

	for _, name := range drop {
		// name прошёл partitionMonth — это валидный `<table>_YYYY_MM`, безопасно инлайнить.
		if _, err := s.db.Exec(ctx, "DROP TABLE IF EXISTS "+name); err != nil {
			return fmt.Errorf("outboxkit: drop partition %s: %w", name, err)
		}
	}
	return nil
}

// partitionMonth парсит `<table>_YYYY_MM` → месяц; ok=false для чужого формата.
func partitionMonth(table, name string) (time.Time, bool) {
	suffix, found := strings.CutPrefix(name, table+"_")
	if !found {
		return time.Time{}, false
	}
	m, err := time.Parse("2006_01", suffix)
	if err != nil {
		return time.Time{}, false
	}
	return m, true
}
