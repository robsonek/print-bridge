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

func TestRenderQualityDefaults(t *testing.T) {
	// Calibrated on a real XP-423B: render to the full 832-dot printhead (margin_x 0)
	// — fit-to-width below the head under-scaled carrier A6 PDFs ~5%, leaving ~1cm
	// blank at the label bottom (dpd_ad, 2026-06-07); threshold 190 (carrier PDFs
	// stroke some frame lines at luma 166/183, dropped at 160). Darkness knobs
	// (^MD/~SD/panel density) proven inert on this firmware (2026-06-07); ^PR works,
	// PR2 kept for a saturated, scannable barcode. See docs/hardware-spike-findings.md.
	c := Default()
	checks := map[string]struct{ got, want int }{
		"RenderThreshold": {c.RenderThreshold, 190},
		"LabelDarkness":   {c.LabelDarkness, 14},
		"PrintSpeedIPS":   {c.PrintSpeedIPS, 2},
		"MarginXDots":     {c.MarginXDots, 0},
		"MarginYDots":     {c.MarginYDots, 8},
		"PrintWidthDots":  {c.PrintWidthDots, 832},
		"RenderWidthDots": {c.RenderWidthDots, 832},
	}
	for name, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("default %s = %d, want %d", name, ck.got, ck.want)
		}
	}
}

func TestRenderQualityFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"print_token":"t","cups_queue":"q","printer_ip":"1.2.3.4","label_darkness":20,"print_speed_ips":3,"render_threshold":140}`), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LabelDarkness != 20 || c.PrintSpeedIPS != 3 || c.RenderThreshold != 140 {
		t.Errorf("file overrides not applied: MD=%d PR=%d thresh=%d", c.LabelDarkness, c.PrintSpeedIPS, c.RenderThreshold)
	}
	// Unspecified render fields keep defaults.
	if c.PrintWidthDots != 832 || c.RenderWidthDots != 832 {
		t.Errorf("unspecified render fields lost defaults: PW=%d RW=%d", c.PrintWidthDots, c.RenderWidthDots)
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

// main.go rzutuje RenderThreshold na uint8 — 256 owija się do 0 i każda
// etykieta wychodzi PUSTA bez żadnego błędu. Walidacja musi to złapać
// na starcie, zanim cokolwiek trafi do druku.
func TestValidateRejectsOutOfRangeRenderThreshold(t *testing.T) {
	for _, bad := range []int{-1, 256, 300} {
		c := validConfig()
		c.RenderThreshold = bad
		if err := c.Validate(); err == nil {
			t.Errorf("render_threshold=%d: Validate musi odrzucić (uint8-truncation = puste etykiety)", bad)
		}
	}
	for _, ok := range []int{0, 1, 190, 255} {
		c := validConfig()
		c.RenderThreshold = ok
		if err := c.Validate(); err != nil {
			t.Errorf("render_threshold=%d: Validate musi przyjąć, got %v", ok, err)
		}
	}
}

// listen_port=0 binduje losowy port efemeryczny — serwis "zdrowy", ale UFW
// przepuszcza tylko 9443, więc nikt się nie dodzwoni.
func TestValidateRejectsOutOfRangeListenPort(t *testing.T) {
	for _, bad := range []int{0, -1, 65536} {
		c := validConfig()
		c.ListenPort = bad
		if err := c.Validate(); err == nil {
			t.Errorf("listen_port=%d: Validate musi odrzucić", bad)
		}
	}
}

func validConfig() Config {
	c := Default()
	c.PrintToken = "t"
	c.CUPSQueue = "q"
	c.PrinterIP = "10.0.0.5"
	return c
}

func TestValidateRejectsEmptyToken(t *testing.T) {
	c := Default()
	c.CUPSQueue = "q"
	c.PrinterIP = "10.0.0.5"
	if err := c.Validate(); err == nil {
		t.Fatal("Validate must reject empty token")
	}
}
