package pgstore

import (
	"embed"
	"fmt"
	"testing/fstest"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations — источник миграций таблицы DefaultTable (таблица версий
// schema_migrations_outboxkit); Module кладёт его в группу pgkit.MigrateModule.
var Migrations = pgkit.Source{Name: "outboxkit", FS: migrationsFS, Dir: "migrations"}

// TableMigrations — источник миграций outbox-таблицы с произвольным именем
// (multi-instance): та же схема, своя таблица версий schema_migrations_<table>.
// Имя длиннее 45 символов pgkit отвергает на прогоне (ограничение Source.Name).
func TableMigrations(table string) pgkit.Source {
	return pgkit.Source{Name: table, FS: tableMigrationsFS(table), Dir: "migrations"}
}

// tableMigrationsFS: DDL целиком идемпотентен (IF NOT EXISTS) — потребитель,
// пришедший со своим зеркалом схемы, применяет источник поверх готовой таблицы.
func tableMigrationsFS(table string) fstest.MapFS {
	return fstest.MapFS{
		"migrations/0001_outbox.up.sql":   &fstest.MapFile{Data: []byte(SchemaSQL(table) + "\n\n" + partitionsDDL(table))},
		"migrations/0001_outbox.down.sql": &fstest.MapFile{Data: fmt.Appendf(nil, "DROP TABLE IF EXISTS %s;\n", table)},
		"migrations/0002_outbox_next_attempt.up.sql": &fstest.MapFile{
			Data: fmt.Appendf(nil, "ALTER TABLE %s ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ;\n", table),
		},
		"migrations/0002_outbox_next_attempt.down.sql": &fstest.MapFile{
			Data: fmt.Appendf(nil, "ALTER TABLE %s DROP COLUMN IF EXISTS next_attempt_at;\n", table),
		},
	}
}

// partitionsDDL — партиции текущего месяца и трёх следующих; дальше их держит
// PartitionKeeperJob.
func partitionsDDL(table string) string {
	return fmt.Sprintf(`DO $$
DECLARE
    base   DATE := DATE_TRUNC('month', NOW())::DATE;
    i      INT;
    pstart DATE;
    pend   DATE;
    pname  TEXT;
BEGIN
    FOR i IN 0..3 LOOP
        pstart := (base + (i || ' month')::INTERVAL)::DATE;
        pend   := (base + ((i + 1) || ' month')::INTERVAL)::DATE;
        pname  := '%[1]s_' || TO_CHAR(pstart, 'YYYY_MM');
        EXECUTE FORMAT(
            'CREATE TABLE IF NOT EXISTS %%I PARTITION OF %[1]s FOR VALUES FROM (%%L) TO (%%L)',
            pname, pstart, pend
        );
    END LOOP;
END $$;
`, table)
}
