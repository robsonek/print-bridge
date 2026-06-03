package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsWhenNoFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("Load with missing file should use defaults, got err: %v", err)
	}
	if c.ListenPort != 9443 {
		t.Errorf("default port = %d, want 9443", c.ListenPort)
	}
	if c.IdempotencyTTLDays != 30 {
		t.Errorf("default ttl = %d, want 30", c.IdempotencyTTLDays)
	}
}

func TestLoadFileThenEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"listen_port":8000,"print_token":"file-tok","cups_queue":"q","printer_ip":"10.0.0.5"}`), 0o600)

	t.Setenv("PRINT_BRIDGE_TOKEN", "env-tok")
	t.Setenv("PRINT_BRIDGE_PORT", "8443")

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PrintToken != "env-tok" {
		t.Errorf("token = %q, want env override 'env-tok'", c.PrintToken)
	}
	if c.ListenPort != 8443 {
		t.Errorf("port = %d, want env override 8443", c.ListenPort)
	}
	if c.CUPSQueue != "q" {
		t.Errorf("queue = %q, want file value 'q'", c.CUPSQueue)
	}
}

func TestValidateRejectsEmptyToken(t *testing.T) {
	c := Default()
	c.CUPSQueue = "q"
	c.PrinterIP = "10.0.0.5"
	if err := c.Validate(); err == nil {
		t.Fatal("Validate must reject empty token")
	}
}
