package outboxkit

import (
	"context"
	"fmt"
	"time"

	"git.stit.tech/stit/golibinfra/pgkit"
)

const schemaSQL = `CREATE TABLE IF NOT EXISTS outbox (
    id           BIGSERIAL    NOT NULL,
    subject      TEXT         NOT NULL,
    payload      JSONB        NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX IF NOT EXISTS outbox_unpublished_idx
    ON outbox (created_at, id) WHERE published_at IS NULL;`

func SchemaSQL() string { return schemaSQL }

func PartitionSQL(month time.Time) (name, sql string) {
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	name = fmt.Sprintf("outbox_%04d_%02d", start.Year(), int(start.Month()))
	sql = fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF outbox FOR VALUES FROM ('%s') TO ('%s')",
		name,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"),
	)
	return name, sql
}

func EnsurePartitions(ctx context.Context, db *pgkit.DB, monthsAhead int) error {
	if monthsAhead < 0 {
		monthsAhead = 0
	}
	now := time.Now().UTC()
	for i := 0; i <= monthsAhead; i++ {
		m := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, i, 0)
		_, sql := PartitionSQL(m)
		if _, err := db.Exec(ctx, sql); err != nil {
			return fmt.Errorf("outboxkit: ensure partition %s: %w", m.Format("2006-01"), err)
		}
	}
	return nil
}
