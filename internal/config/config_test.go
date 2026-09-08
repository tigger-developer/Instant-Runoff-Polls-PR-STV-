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
moderators: []
auth:
  key_id: key-1
  signing_key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
smtp:
  host: 127.0.0.1
  port: 1025
  from: polls@example.test
  tls_mode: development_plain
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

func TestLoadValidatesWorkflowConfiguration(t *testing.T) {
	directory := t.TempDir()
	defaults := filepath.Join(directory, "defaults.yaml")
	writeConfig(t, defaults, validDefaults)
	cfg, err := Load(defaults, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.KeyID != "key-1" || len(cfg.Auth.SigningKey) != 32 || cfg.SMTP.Port != 1025 || cfg.HTTP.SecureCookies {
		t.Fatalf("workflow configuration = %#v", cfg)
	}

	for name, overlay := range map[string]string{
		"duplicate moderator":   "moderators:\n  - {id: one, email: SAME@example.test}\n  - {id: two, email: same@example.test}\n",
		"invalid signing key":   "auth:\n  signing_key: c2hvcnQ=\n",
		"plaintext remote smtp": "smtp:\n  host: smtp.example.test\n  tls_mode: development_plain\n",
		"partial credentials":   "smtp:\n  username: user\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, name+".yaml")
			writeConfig(t, path, overlay)
			if _, err := Load(defaults, path, ""); err == nil {
				t.Fatal("expected invalid workflow configuration")
			}
		})
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
		{name: "non-string mapping key", contents: "1: value\n", want: "parse configuration layer"},
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

func TestLoadHandlesEmptyOptionalOverlayAndRejectsEmptyDefaults(t *testing.T) {
	directory := t.TempDir()
	defaults := filepath.Join(directory, "defaults.yaml")
	overlay := filepath.Join(directory, "overlay.yaml")
	writeConfig(t, defaults, validDefaults)
	writeConfig(t, overlay, "")
	if _, err := Load(defaults, overlay, ""); err != nil {
		t.Fatalf("empty optional overlay: %v", err)
	}
	empty := filepath.Join(directory, "empty.yaml")
	writeConfig(t, empty, "")
	if _, err := Load(empty, "", ""); err == nil || !strings.Contains(err.Error(), "must be a mapping") {
		t.Fatalf("empty defaults error = %v", err)
	}
}

func TestLoadRejectsEveryInvalidHTTPDuration(t *testing.T) {
	directory := t.TempDir()
	defaults := filepath.Join(directory, "defaults.yaml")
	writeConfig(t, defaults, validDefaults)
	for _, key := range []string{"read_header_timeout", "read_timeout", "write_timeout", "idle_timeout", "shutdown_timeout"} {
		t.Run(key, func(t *testing.T) {
			overlay := filepath.Join(directory, key+".yaml")
			writeConfig(t, overlay, "http:\n  "+key+": zero\n")
			_, err := Load(defaults, overlay, "")
			if err == nil || !strings.Contains(err.Error(), "http."+key) {
				t.Fatalf("invalid %s error = %v", key, err)
			}
		})
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
