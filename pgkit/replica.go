package pgkit

import "fmt"

// ReplicaConfig — опциональная read-replica; пустой ReplicaDSN означает «реплики
// нет» и Read() идёт на мастер.
type ReplicaConfig struct {
	ReplicaDSN string `envconfig:"PG_REPLICA_DSN"`
	MaxConns   int32  `envconfig:"PG_REPLICA_MAX_CONNS" default:"10"`
}

// Validate проверяет инварианты конфига; нарушение — ErrConfig. Zero value
// валиден: это «реплики нет».
func (c ReplicaConfig) Validate() error {
	if c.MaxConns < 0 {
		return fmt.Errorf("%w: PG_REPLICA_MAX_CONNS must be >= 0, got %d", ErrConfig, c.MaxConns)
	}
	return nil
}

// poolConfig выводит параметры пула реплики из параметров primary: своими у
// реплики остаются только DSN и MaxConns, остальное (таймауты, MinConns,
// трассировка) наследуется. Нарушение инвариантов — ErrConfig с именами
// PG_REPLICA_*: оператор менял их, а не PG_MAX_CONNS.
func (c ReplicaConfig) poolConfig(base PoolConfig) (PoolConfig, error) {
	if c.MaxConns > 0 && base.MinConns > c.MaxConns {
		return PoolConfig{}, fmt.Errorf("%w: PG_REPLICA_MAX_CONNS (%d) must be >= PG_MIN_CONNS (%d)",
			ErrConfig, c.MaxConns, base.MinConns)
	}
	base.DSN = c.ReplicaDSN
	base.MaxConns = c.MaxConns
	return base, nil
}

// ReadWriteDB — пара пулов read/write; при отсутствии реплики обе стороны
// указывают на мастер.
type ReadWriteDB struct {
	Master  *DB
	Replica *DB
}

// NewReadWriteDB собирает пару; replica == nil означает «реплики нет».
func NewReadWriteDB(master, replica *DB) *ReadWriteDB {
	if replica == nil {
		replica = master
	}
	return &ReadWriteDB{Master: master, Replica: replica}
}

// Read отдаёт реплику (мастер, если реплики нет).
func (rw *ReadWriteDB) Read() *DB {
	return rw.Replica
}

// Write отдаёт мастер.
func (rw *ReadWriteDB) Write() *DB {
	return rw.Master
}

// HasReplica сообщает, поднят ли отдельный пул реплики.
func (rw *ReadWriteDB) HasReplica() bool {
	return rw.Replica != nil && rw.Replica != rw.Master
}
