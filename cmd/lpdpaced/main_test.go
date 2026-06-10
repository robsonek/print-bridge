package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// Pusty job i nieczytelny spool to błędy TRWAŁE — ponowienie nic nie zmieni.
// exitFailed (1) oddaje decyzję error-policy CUPS (potrafi zatrzymać kolejkę
// albo zapętlić job); CUPS_BACKEND_CANCEL (5) bezwarunkowo kasuje job.
func TestRunEmptyStdinJobIsCanceledNotRetried(t *testing.T) {
	t.Setenv("DEVICE_URI", "lpdpaced://127.0.0.1/lp?rate=0")
	var stderr bytes.Buffer
	code := runWith([]string{"lpdpaced", "1", "user", "title", "1", ""}, strings.NewReader(""), &stderr)
	if code != exitCancel {
		t.Errorf("pusty job: exit = %d, want exitCancel(%d); stderr: %s", code, exitCancel, stderr.String())
	}
}

func TestRunUnreadableSpoolFileIsCanceledNotRetried(t *testing.T) {
	t.Setenv("DEVICE_URI", "lpdpaced://127.0.0.1/lp?rate=0")
	var stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "no-such-spool")
	code := runWith([]string{"lpdpaced", "1", "user", "title", "1", "", missing}, strings.NewReader(""), &stderr)
	if code != exitCancel {
		t.Errorf("nieczytelny spool: exit = %d, want exitCancel(%d); stderr: %s", code, exitCancel, stderr.String())
	}
}

// Nieparsowalne copies z argv CUPS-a nie może być cicho połknięte jako 0 —
// jedna kopia ma się wydrukować, ale operator musi zobaczyć ostrzeżenie.
func TestParseCopiesGarbageWarnsAndDefaultsToOne(t *testing.T) {
	var stderr bytes.Buffer
	if got := parseCopies("abc", &stderr); got != 1 {
		t.Errorf("parseCopies(garbage) = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("nieparsowalne copies musi zostawić WARNING na stderr, got: %q", stderr.String())
	}
}

func TestParseCopiesValid(t *testing.T) {
	var stderr bytes.Buffer
	if got := parseCopies("3", &stderr); got != 3 {
		t.Errorf("parseCopies(3) = %d, want 3", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("poprawne copies nie może ostrzegać: %q", stderr.String())
	}
}
