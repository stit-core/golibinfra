package pgstore

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// Module — fx-вход кита: *Store и он же как auditkit.Logger, миграции кита в группе
// pgkit.MigrateModule и проверка схемы в OnStart (после них, если модуль в графе). Нужен
// *pgkit.DB от caller'а (pgkit.Module). Записи не удаляет — срок хранения даёт RetentionModule.
var Module = fx.Module(
	"auditkit/pgstore",
	fx.Provide(
		NewStore,
		func(s *Store) auditkit.Logger { return s },
	),
	pgkit.SupplyMigrations(Migrations),
	pgkit.CheckSchemaOnStart(Migrations),
)
