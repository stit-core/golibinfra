package pgkit

import (
	"context"
	"fmt"

	"git.stit.tech/stit-core/golibinfra/healthkit"
)

// Probe — health-проба пула primary для fx-группы healthkit.Group.
func Probe(db *DB) healthkit.Probe {
	return healthkit.New("pgkit", func(ctx context.Context) error {
		var one int
		if err := db.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
			return fmt.Errorf("pgkit: ping: %w", err)
		}
		return nil
	})
}

// ReplicaProbe — health-проба пула реплики. Без отдельной реплики проверка
// проходит без запроса: мастер уже покрыт Probe.
func ReplicaProbe(rw *ReadWriteDB) healthkit.Probe {
	return healthkit.New("pgkit/replica", func(ctx context.Context) error {
		if !rw.HasReplica() {
			return nil
		}
		var one int
		if err := rw.Read().QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
			return fmt.Errorf("pgkit: replica ping: %w", err)
		}
		return nil
	})
}
