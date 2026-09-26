package hub

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestSQLitePersistenceRollbackAndConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory with spaces", "infra.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.DB.Close() }()
	// Every pooled connection must have the same durability and busy handling.
	var release []func() error
	for range 4 {
		conn, err := store.DB.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		release = append(release, conn.Close)
		var mode string
		var timeout, synchronous int
		if err = conn.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if err = conn.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if err = conn.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatal(err)
		}
		if mode != "wal" || timeout != 5000 || synchronous != 2 {
			t.Fatalf("settings: %s %d %d", mode, timeout, synchronous)
		}

	}
	for _, close := range release {
		close()
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := store.Update(t.Context(), func(s *State) error {
				id := fmt.Sprintf("repo-%d", i)
				s.Repositories[id] = Repository{ID: id}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
			if _, err = store.Fleet(t.Context()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// A read transaction retains its snapshot without blocking a writer.
	tx, err := store.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(t.Context(), "SELECT count(*) FROM repositories").Scan(&count); err != nil || count != 20 {
		t.Fatalf("count %d: %v", count, err)
	}
	if err = store.Update(t.Context(), func(s *State) error { s.Repositories["new"] = Repository{ID: "new"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRowContext(t.Context(), "SELECT count(*) FROM repositories").Scan(&count); err != nil || count != 20 {
		t.Fatalf("read snapshot changed: %d %v", count, err)
	}
	tx.Rollback()
	rejected := errors.New("reject")
	if err = store.Update(t.Context(), func(s *State) error { delete(s.Repositories, "new"); return rejected }); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	store.DB.Close()
	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Read(t.Context())
	if err != nil || len(state.Repositories) != 21 {
		t.Fatalf("persisted records: %v %v", state, err)
	}
}
