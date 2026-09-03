package pgstore

import (
	"embed"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations — источник миграций single-instance таблицы DefaultTable (таблица
// версий schema_migrations_outboxkit); Module кладёт его в группу pgkit.MigrateModule.
var Migrations = pgkit.Source{Name: "outboxkit", FS: migrationsFS, Dir: "migrations"}
