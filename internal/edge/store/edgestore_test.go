package edgestore

import (
	"context"
	"testing"
)

// Every connection keeps SQLite's temporary tables and indexes in memory,
// so a read-only root filesystem cannot fail a query.
func TestTemporaryStorageIsInMemory(t *testing.T) {
	s := openStore(t)
	s.db.SetMaxIdleConns(0)
	for range 3 {
		var mode int
		if err := s.db.QueryRowContext(context.Background(), `PRAGMA temp_store`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != 2 {
			t.Fatalf("PRAGMA temp_store = %d, want 2 (memory)", mode)
		}
	}
}
