// Package config loads agent configuration from a JSON file with environment overrides.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
)

type Config struct {
	ListenPort         int    `json:"listen_port"`
	PrintToken         string `json:"print_token"`
	CUPSQueue          string `json:"cups_queue"`
	PrinterIP          string `json:"printer_ip"`
	TLSCertPath        string `json:"tls_cert_path"`
	TLSKeyPath         string `json:"tls_key_path"`
	IdempotencyDB      string `json:"idempotency_db"`
	IdempotencyTTLDays int    `json:"idempotency_ttl_days"`
	ConfirmTimeoutSec  int    `json:"confirm_timeout_sec"`
	LabelWidthMM       int    `json:"label_width_mm"`
	LabelHeightMM      int    `json:"label_height_mm"`
	RenderDPI          int    `json:"render_dpi"`
}

func Default() Config {
	return Config{
		ListenPort:         9443,
		TLSCertPath:        "data/cert.pem",
		TLSKeyPath:         "data/key.pem",
		IdempotencyDB:      "data/idempotency.db",
		IdempotencyTTLDays: 30,
		ConfirmTimeoutSec:  30,
		LabelWidthMM:       104,
		LabelHeightMM:      148,
		RenderDPI:          203,
	}
}

// Load reads the JSON file (if present) over defaults, then applies env overrides.
func Load(path string) (Config, error) {
	c := Default()
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, err
		}
	} else if !os.IsNotExist(err) {
		return c, err
	}
	applyEnv(&c)
	return c, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("PRINT_BRIDGE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.ListenPort = n
		}
	}
	if v := os.Getenv("PRINT_BRIDGE_TOKEN"); v != "" {
		c.PrintToken = v
	}
	if v := os.Getenv("PRINT_BRIDGE_CUPS_QUEUE"); v != "" {
		c.CUPSQueue = v
	}
	if v := os.Getenv("PRINT_BRIDGE_PRINTER_IP"); v != "" {
		c.PrinterIP = v
	}
}

func (c Config) Validate() error {
	if c.PrintToken == "" {
		return errors.New("print_token is required")
	}
	if c.CUPSQueue == "" {
		return errors.New("cups_queue is required")
	}
	if c.PrinterIP == "" {
		return errors.New("printer_ip is required")
	}
	return nil
}
