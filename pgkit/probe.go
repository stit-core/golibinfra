package pgkit

import (
	"context"
)

func DBProbe(db *DB) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		var one int
		return db.QueryRow(ctx, "SELECT 1").Scan(&one)
	}
}
