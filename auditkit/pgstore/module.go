package pgstore

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// Module — fx-вход кита: *Store как auditkit.Logger, миграции кита в группе
// pgkit.MigrateModule и проверка схемы в OnStart (после них, если модуль в графе). Нужен
// *pgkit.DB от caller'а (pgkit.Module).
var Module = fx.Module(
	"auditkit/pgstore",
	fx.Provide(
		fx.Annotate(NewStore, fx.As(new(auditkit.Logger))),
	),
	pgkit.SupplyMigrations(Migrations),
	pgkit.CheckSchemaOnStart(Migrations),
)
