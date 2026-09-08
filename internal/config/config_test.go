package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validDefaults = `base_url: https://defaults.example
data_dirs: [templates, static]
http:
  read_header_timeout: 5s
  read_timeout: 15s
  write_timeout: 15s
  idle_timeout: 60s
  shutdown_timeout: 10s
`

func TestLoadMergesLayersRecursively(t *testing.T) {
	directory := t.TempDir()
	defaults := filepath.Join(directory, "defaults.yaml")
	host := filepath.Join(directory, "host.yaml")
	secrets := filepath.Join(directory, "secrets.yaml")
	writeConfig(t, defaults, validDefaults)
	writeConfig(t, host, "base_url: https://host.example\nhttp:\n  read_timeout: 20s\n")
	writeConfig(t, secrets, "http:\n  idle_timeout: 90s\n")

	cfg, err := Load(defaults, host, secrets)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.BaseURL != "https://host.example" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if got := cfg.HTTP.ReadTimeout.String(); got != "20s" {
		t.Fatalf("ReadTimeout = %s", got)
	}
	if got := cfg.HTTP.IdleTimeout.String(); got != "1m30s" {
		t.Fatalf("IdleTimeout = %s", got)
	}
}

func TestLoadRejectsInvalidLayersWithoutLeakingSecretValues(t *testing.T) {
	directory := t.TempDir()
	defaults := filepath.Join(directory, "defaults.yaml")
	writeConfig(t, defaults, validDefaults)

	cases := []struct {
		name     string
		contents string
		want     string
	}{
		{name: "invalid duration", contents: "http:\n  read_timeout: no-time\n", want: "http.read_timeout"},
		{name: "duplicate key", contents: "base_url: https://one.example\nbase_url: https://two.example\n", want: "parse configuration layer"},
		{name: "multiple documents", contents: "base_url: https://one.example\n---\nbase_url: https://two.example\n", want: "multiple documents"},
		{name: "wrong type", contents: "data_dirs: wrong\n", want: "data_dirs"},
		{name: "null required value", contents: "base_url: null\n", want: "base_url"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			override := filepath.Join(directory, tc.name+".yaml")
			writeConfig(t, override, tc.contents)
			_, err := Load(defaults, override, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want %q", err, tc.want)
			}
		})
	}

	secret := filepath.Join(directory, "secret.yaml")
	writeConfig(t, secret, "token: very-secret-value\nhttp:\n  read_timeout: no-time\n")
	_, err := Load(defaults, "", secret)
	if err == nil || strings.Contains(err.Error(), "very-secret-value") {
		t.Fatalf("secret diagnostic = %v", err)
	}
}

func TestLoadRejectsMissingOrUnreadableRequiredLayers(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.yaml")
	if _, err := Load("", "", ""); err == nil || !strings.Contains(err.Error(), "defaults") {
		t.Fatalf("missing defaults error = %v", err)
	}
	if _, err := Load(missing, "", ""); err == nil || !strings.Contains(err.Error(), "read configuration layer") {
		t.Fatalf("unreadable defaults error = %v", err)
	}
	defaults := filepath.Join(directory, "defaults.yaml")
	writeConfig(t, defaults, validDefaults)
	if _, err := Load(defaults, missing, ""); err == nil || !strings.Contains(err.Error(), "read configuration layer") {
		t.Fatalf("unreadable explicit host error = %v", err)
	}
}

func writeConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
