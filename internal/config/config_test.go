package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMergesLayersAndRejectsInvalidDuration(t *testing.T) {
	directory := t.TempDir()
	defaults := filepath.Join(directory, "defaults.yaml")
	override := filepath.Join(directory, "override.yaml")
	if err := os.WriteFile(defaults, []byte("base_url: http://default\ndata_dirs: [templates, static]\nhttp:\n  read_header_timeout: 5s\n  read_timeout: 15s\n  write_timeout: 15s\n  idle_timeout: 60s\n  shutdown_timeout: 10s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("base_url: http://override\nhttp:\n  read_timeout: nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(defaults, "", "")
	if err != nil || cfg.BaseURL != "http://default" {
		t.Fatalf("Load(defaults) = %#v, %v", cfg, err)
	}
	_, err = Load(defaults, override, "")
	if err == nil || !strings.Contains(err.Error(), "http.read_timeout") {
		t.Fatalf("invalid duration error = %v", err)
	}
}
