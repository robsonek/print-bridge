package idempotency

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "idem.db"), 30)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMissingKey(t *testing.T) {
	s := openTemp(t)
	_, found, err := s.Get("pj:1")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("missing key must report found=false")
	}
}

func TestPendingThenTerminalReplay(t *testing.T) {
	s := openTemp(t)
	if err := s.SavePending("pj:1", "cups-7"); err != nil {
		t.Fatal(err)
	}
	rec, found, _ := s.Get("pj:1")
	if !found || rec.Terminal || rec.CUPSJobID != "cups-7" {
		t.Fatalf("pending record wrong: %+v found=%v", rec, found)
	}
	if err := s.SaveTerminal("pj:1", `{"status":"printed"}`); err != nil {
		t.Fatal(err)
	}
	rec, found, _ = s.Get("pj:1")
	if !found || !rec.Terminal || rec.ResponseJSON != `{"status":"printed"}` {
		t.Fatalf("terminal record wrong: %+v", rec)
	}
}

// #22: a row whose created_at is not parseable RFC3339 must be treated as
// expired/corrupt — Get must report found=false, not return an un-expiring record.
func TestGetUnparsableCreatedAtTreatedAsAbsent(t *testing.T) {
	s := openTemp(t)
	if err := s.SavePending("pj:bad", "cups-9"); err != nil {
		t.Fatal(err)
	}
	// Corrupt the created_at to a non-RFC3339 value.
	if _, err := s.db.Exec(
		`UPDATE idempotency SET created_at = 'not-a-timestamp' WHERE key = 'pj:bad'`,
	); err != nil {
		t.Fatal(err)
	}
	rec, found, err := s.Get("pj:bad")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if found {
		t.Fatalf("record with unparseable created_at must report found=false, got %+v", rec)
	}
}

// #12/#19: concurrent SavePending from many goroutines must not surface
// "database is locked" (SQLITE_BUSY). SetMaxOpenConns(1) serializes writers.
func TestConcurrentSavePendingNoLockError(t *testing.T) {
	s := openTemp(t)
	const writers = 16
	const perWriter = 50
	var wg sync.WaitGroup
	errCh := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				key := fmt.Sprintf("pj:%d-%d", w, i)
				if err := s.SavePending(key, fmt.Sprintf("cups-%d-%d", w, i)); err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent SavePending failed (database is locked?): %v", err)
	}
}

func TestCleanupRemovesExpired(t *testing.T) {
	s := openTemp(t)
	s.SaveTerminal("old", `{}`)
	// Backdate the row beyond TTL.
	if _, err := s.db.Exec(`UPDATE idempotency SET created_at = ? WHERE key = 'old'`,
		time.Now().Add(-40*24*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	s.SaveTerminal("fresh", `{}`)
	n, err := s.Cleanup(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("cleaned %d rows, want 1", n)
	}
	if _, found, _ := s.Get("fresh"); !found {
		t.Error("fresh row must survive cleanup")
	}
}
