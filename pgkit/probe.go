package pgkit

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/healthkit"
)

func DBProbe(db *DB) healthkit.Probe {
	return func(ctx context.Context) error {
		var one int
		return db.QueryRow(ctx, "SELECT 1").Scan(&one)
	}
}
