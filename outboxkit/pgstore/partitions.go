package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// ErrPendingRows — retention пропустил партиции с неопубликованными событиями;
// текст ошибки перечисляет их.
var ErrPendingRows = errors.New("outboxkit/pgstore: partition holds unpublished events")

// SchemaSQL возвращает DDL партиционированной таблицы outbox + индекс для
// заданного имени. Обычно не нужен напрямую: миграции кастомной таблицы даёт
// TableMigrations(table), таблицу DefaultTable заводит Migrations.
func SchemaSQL(table string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s (
    id              BIGSERIAL    NOT NULL,
    subject         TEXT         NOT NULL,
    payload         JSONB        NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    published_at    TIMESTAMPTZ,
    attempts        INT          NOT NULL DEFAULT 0,
    last_error      TEXT,
    next_attempt_at TIMESTAMPTZ,
    dead_at         TIMESTAMPTZ,
    trace_context   JSONB,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX IF NOT EXISTS %[1]s_unpublished_idx
    ON %[1]s (created_at, id) WHERE published_at IS NULL AND dead_at IS NULL;`, table)
}

// PartitionSQL возвращает имя месячной партиции таблицы и DDL её создания;
// month усекается до начала месяца.
func PartitionSQL(table string, month time.Time) (name, sql string) {
	start := monthStart(month)
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

// monthStart — первый день месяца в UTC: AddDate нормализует 31-е число
// («31 января + 1 месяц» = 3 марта), поэтому арифметика идёт от начала месяца.
func monthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// CheckSchema сверяет схему таблицы с источником её миграций (Migrations у NewStore,
// TableMigrations у NewStoreFor): без миграций — pgkit.ErrSchema с именем источника.
func (s *Store) CheckSchema(ctx context.Context) error {
	if err := pgkit.CheckSchema(ctx, s.db, s.src); err != nil {
		return fmt.Errorf("outboxkit/pgstore: table %s: %w", s.table, err)
	}
	return nil
}

// EnsurePartitions создаёт партиции текущего и monthsAhead следующих месяцев;
// идемпотентна и не падает, когда ту же партицию параллельно создаёт другой
// процесс (хук и keeper-job на старте, вторая реплика).
func (s *Store) EnsurePartitions(ctx context.Context, monthsAhead int) error {
	if monthsAhead < 0 {
		monthsAhead = 0
	}
	base := monthStart(s.clock.Now())
	for i := 0; i <= monthsAhead; i++ {
		m := base.AddDate(0, i, 0)
		_, sql := PartitionSQL(s.table, m)
		if _, err := s.db.Exec(ctx, sql); err != nil && !createdConcurrently(err) {
			return fmt.Errorf("outboxkit/pgstore: ensure partition %s: %w", m.Format("2006-01"), err)
		}
	}
	return nil
}

// createdConcurrently: IF NOT EXISTS смотрит на имя до блокировки родителя, а
// блокировка родителя сериализует создателей партиций — проигравший гонку видит
// закоммиченное имя и получает duplicate_table: партиция уже есть.
func createdConcurrently(err error) bool {
	pg, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pg.Code == "42P07"
}

// listPartitionsSQL берёт и партиции родителя, и сирот — таблицы с именем
// партиции, которые уже ни к чему не подключены: успевший DETACH с сорвавшимся
// DROP иначе навсегда выпадает из retention.
const listPartitionsSQL = `WITH parent AS (SELECT to_regclass($1) AS oid)
SELECT c.relname, true AS attached, i.inhdetachpending
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
WHERE i.inhparent = (SELECT oid FROM parent)
UNION ALL
SELECT c.relname, false AS attached, false AS inhdetachpending
FROM pg_class c, parent p
WHERE p.oid IS NOT NULL
  AND c.oid <> p.oid
  AND c.relkind IN ('r', 'p')
  AND c.relnamespace = (SELECT relnamespace FROM pg_class WHERE oid = p.oid)
  AND c.relname ~ ('^' || $1 || '_[0-9]{4}_[0-9]{2}$')
  AND NOT EXISTS (SELECT 1 FROM pg_inherits x WHERE x.inhrelid = c.oid)`

type partitionRef struct {
	name          string
	attached      bool
	detachPending bool
}

// DropOldPartitions дропает партиции старше keepMonths месяцев (<= 0 — no-op);
// партиции с неопубликованными событиями остаются и перечисляются в
// ErrPendingRows. Вызывается вне транзакции: DETACH CONCURRENTLY недопустим в
// transaction block.
func (s *Store) DropOldPartitions(ctx context.Context, keepMonths int) error {
	if keepMonths <= 0 {
		return nil
	}
	if pgkit.InTx(ctx) {
		return errors.New("outboxkit/pgstore: DropOldPartitions must run outside a transaction")
	}
	cutoff := monthStart(s.clock.Now()).AddDate(0, -keepMonths, 0)

	drop, err := s.oldPartitions(ctx, cutoff)
	if err != nil {
		return err
	}

	var pending []string
	for _, p := range drop {
		// name прошёл partitionMonth — это валидный `<table>_YYYY_MM`, безопасно инлайнить.
		var hasPending bool
		if err := s.db.QueryRow(ctx, fmt.Sprintf(s.pendingSQLFmt, p.name)).Scan(&hasPending); err != nil {
			return fmt.Errorf("outboxkit/pgstore: check partition %s: %w", p.name, err)
		}
		if hasPending {
			pending = append(pending, p.name)
			continue
		}
		if p.attached {
			if err := s.detachPartition(ctx, p); err != nil {
				return err
			}
		}
		if _, err := s.db.Exec(ctx, "DROP TABLE IF EXISTS "+p.name); err != nil {
			return fmt.Errorf("outboxkit/pgstore: drop partition %s: %w", p.name, err)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("%w: %s", ErrPendingRows, strings.Join(pending, ", "))
	}
	return nil
}

func (s *Store) oldPartitions(ctx context.Context, cutoff time.Time) ([]partitionRef, error) {
	rows, err := s.db.Query(ctx, listPartitionsSQL, s.table)
	if err != nil {
		return nil, fmt.Errorf("outboxkit/pgstore: list partitions: %w", err)
	}
	defer rows.Close()
	var out []partitionRef
	for rows.Next() {
		var p partitionRef
		if err := rows.Scan(&p.name, &p.attached, &p.detachPending); err != nil {
			return nil, fmt.Errorf("outboxkit/pgstore: scan partition: %w", err)
		}
		if m, ok := partitionMonth(s.table, p.name); !ok || !m.Before(cutoff) {
			continue
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outboxkit/pgstore: list partitions: %w", err)
	}
	return out, nil
}

// detachPartition отцепляет партицию без ACCESS EXCLUSIVE на родителе: обычный
// DROP выстроил бы за собой очередь из вставок доменных транзакций и тиков.
func (s *Store) detachPartition(ctx context.Context, p partitionRef) error {
	stmt := fmt.Sprintf("ALTER TABLE %s DETACH PARTITION %s CONCURRENTLY", s.table, p.name)
	if p.detachPending {
		// Прерванный detach оставляет партицию в detach-pending: CONCURRENTLY на
		// ней отказывает, завершает такую партицию только FINALIZE.
		stmt = fmt.Sprintf("ALTER TABLE %s DETACH PARTITION %s FINALIZE", s.table, p.name)
	}
	if _, err := s.db.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("outboxkit/pgstore: detach partition %s: %w", p.name, err)
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
