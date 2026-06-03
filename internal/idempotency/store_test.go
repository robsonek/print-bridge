package idempotency

import (
	"path/filepath"
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
