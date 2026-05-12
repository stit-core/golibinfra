package pgstore

import "embed"

//go:embed migrations/*.sql
var MigrationsFS embed.FS
