package pgstore

import (
	"embed"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations — источник миграций кита (таблица версий
// schema_migrations_authnkit_session); Module кладёт его в группу pgkit.MigrateModule.
var Migrations = pgkit.Source{Name: "authnkit_session", FS: migrationsFS, Dir: "migrations"}
