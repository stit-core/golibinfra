package pgkit

type ReplicaConfig struct {
	ReplicaDSN  string `envconfig:"PG_REPLICA_DSN"`
	MaxConns    int32  `envconfig:"PG_REPLICA_MAX_CONNS" default:"10"`
}

type ReadWriteDB struct {
	Master  *DB
	Replica *DB
}

func NewReadWriteDB(master, replica *DB) *ReadWriteDB {
	if replica == nil {
		replica = master
	}
	return &ReadWriteDB{Master: master, Replica: replica}
}

func (rw *ReadWriteDB) Read() *DB {
	return rw.Replica
}

func (rw *ReadWriteDB) Write() *DB {
	return rw.Master
}
