package update

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidateTag(t *testing.T) {
	valid := []string{"v1.2.3", "1.2.3", "v0.7.44", "v1.0.0-rc1"}
	for _, tag := range valid {
		if err := ValidateTag(tag); err != nil {
			t.Errorf("ValidateTag(%q) = %v, want nil", tag, err)
		}
	}
	invalid := []string{"", "latest; rm -rf /", "v1", "$(whoami)", "1.2.3 && curl evil"}
	for _, tag := range invalid {
		if err := ValidateTag(tag); err == nil {
			t.Errorf("ValidateTag(%q) = nil, want error (injection guard)", tag)
		}
	}
}

func TestSpawnUpdaterRunsViaSudoAndLogs(t *testing.T) {
	dir := t.TempDir()
	// Podstawka za sudo: zapisuje swoje argv na stdout (który SpawnUpdater
	// przekierowuje do pliku logu) — bez realnego sudo w testach.
	fake := dir + "/fake-sudo"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"FAKE-SUDO $@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := sudoBin
	sudoBin = fake
	t.Cleanup(func() { sudoBin = orig })

	logPath := dir + "/update.log"
	if err := SpawnUpdater("/usr/local/sbin/update-bridge.sh", logPath, "v1.2.3"); err != nil {
		t.Fatalf("SpawnUpdater: %v", err)
	}

	// Start() jest asynchroniczny — czekaj aż wyjście podstawki trafi do logu.
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(logPath)
		s := string(b)
		if strings.Contains(s, "FAKE-SUDO -n /usr/local/sbin/update-bridge.sh v1.2.3") {
			if !strings.Contains(s, "spawn updater tag=v1.2.3") {
				t.Errorf("log bez nagłówka spawnu: %q", s)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("log nie zawiera wywołania przez sudo: %q", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSpawnUpdaterRejectsBadTagBeforeSpawning(t *testing.T) {
	dir := t.TempDir()
	logPath := dir + "/update.log"
	if err := SpawnUpdater("/usr/local/sbin/update-bridge.sh", logPath, "latest; rm -rf /"); err == nil {
		t.Fatal("zły tag musi być odrzucony przed spawnem")
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("zły tag nie powinien nawet tworzyć logu")
	}
}

func TestSpawnUpdaterUnwritableLogIsError(t *testing.T) {
	err := SpawnUpdater("/usr/local/sbin/update-bridge.sh", "/nonexistent-dir/update.log", "v1.2.3")
	if err == nil {
		t.Fatal("niezapisywalny log musi zwrócić błąd (inaczej updater znowu umiera po cichu)")
	}
}
