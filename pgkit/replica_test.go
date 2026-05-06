package pgkit

import (
	"testing"
)

func TestNewReadWriteDB_WithReplica(t *testing.T) {
	master := &DB{}
	replica := &DB{}

	rw := NewReadWriteDB(master, replica)

	if rw.Read() != replica {
		t.Error("Read() should return replica")
	}
	if rw.Write() != master {
		t.Error("Write() should return master")
	}
}

func TestNewReadWriteDB_NoReplica(t *testing.T) {
	master := &DB{}

	rw := NewReadWriteDB(master, nil)

	if rw.Read() != master {
		t.Error("Read() should return master when no replica")
	}
	if rw.Write() != master {
		t.Error("Write() should return master")
	}
}
