// Package projection — эталон схемы локальных read-проекций хаба: миграции и
// sqlc-запросы companies/projects/employees/visibility как шаблон и как
// применимый источник pgkit.
package projection

import (
	"embed"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// MigrationsFS — SQL-шаблоны миграций проекций (templates/*.sql).
//
//go:embed templates/*.sql
var MigrationsFS embed.FS

// QueriesFS — эталонные sqlc-запросы (queries/*.sql): upsert из фида + листинги.
//
//go:embed queries/*.sql
var QueriesFS embed.FS

// Migrations — шаблон как готовый источник pgkit (таблица версий
// schema_migrations_hub_projection). Альтернатива копированию файлов: продукт,
// применяющий шаблон напрямую, не расходится с ним со временем.
var Migrations = pgkit.Source{Name: "hub_projection", FS: MigrationsFS, Dir: "templates"}
