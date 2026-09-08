// ABOUTME: Loads and validates layered runtime configuration for STV Poll.
// ABOUTME: Keeps deployment-owned paths and application settings explicit.
package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"time"
)

type HTTP struct {
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	SecureCookies     bool          `yaml:"secure_cookies"`
}

func (h *HTTP) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		ReadHeaderTimeout string `yaml:"read_header_timeout"`
		ReadTimeout       string `yaml:"read_timeout"`
		WriteTimeout      string `yaml:"write_timeout"`
		IdleTimeout       string `yaml:"idle_timeout"`
		ShutdownTimeout   string `yaml:"shutdown_timeout"`
		SecureCookies     bool   `yaml:"secure_cookies"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	parse := func(name, value string) (time.Duration, error) {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return 0, fmt.Errorf("configuration field %s: %w", name, err)
		}
		return parsed, nil
	}
	var err error
	if h.ReadHeaderTimeout, err = parse("http.read_header_timeout", raw.ReadHeaderTimeout); err != nil {
		return err
	}
	if h.ReadTimeout, err = parse("http.read_timeout", raw.ReadTimeout); err != nil {
		return err
	}
	if h.WriteTimeout, err = parse("http.write_timeout", raw.WriteTimeout); err != nil {
		return err
	}
	if h.IdleTimeout, err = parse("http.idle_timeout", raw.IdleTimeout); err != nil {
		return err
	}
	if h.ShutdownTimeout, err = parse("http.shutdown_timeout", raw.ShutdownTimeout); err != nil {
		return err
	}
	h.SecureCookies = raw.SecureCookies
	return nil
}

type Moderator struct {
	ID    string `yaml:"id"`
	Email string `yaml:"email"`
}
type Auth struct {
	KeyID          string `yaml:"key_id"`
	SigningKeyText string `yaml:"signing_key"`
	SigningKey     []byte `yaml:"-"`
}
type SMTP struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	From     string `yaml:"from"`
	TLSMode  string `yaml:"tls_mode"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type Config struct {
	BaseURL    string      `yaml:"base_url"`
	DataDirs   []string    `yaml:"data_dirs"`
	HTTP       HTTP        `yaml:"http"`
	Moderators []Moderator `yaml:"moderators"`
	Auth       Auth        `yaml:"auth"`
	SMTP       SMTP        `yaml:"smtp"`
}

func Load(defaultsPath, configPath, secretsPath string) (Config, error) {
	var merged map[string]any
	for _, layer := range []struct {
		path     string
		required bool
	}{{defaultsPath, true}, {configPath, false}, {secretsPath, false}} {
		if layer.path == "" {
			if layer.required {
				return Config{}, errors.New("configuration defaults path is required")
			}
			continue
		}
		data, err := os.ReadFile(layer.path)
		if err != nil {
			return Config{}, fmt.Errorf("read configuration layer %q: %w", layer.path, err)
		}
		values, err := parseLayer(data)
		if err != nil {
			return Config{}, fmt.Errorf("parse configuration layer %q: %w", layer.path, err)
		}
		if values == nil {
			if layer.required {
				return Config{}, fmt.Errorf("configuration layer %q must be a mapping", layer.path)
			}
			continue
		}
		if merged == nil {
			merged = map[string]any{}
		}
		merge(merged, values)
	}
	if err := validateKnownTypes(merged); err != nil {
		return Config{}, err
	}
	data, err := yaml.Marshal(merged)
	if err != nil {
		return Config{}, fmt.Errorf("encode merged configuration: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode application configuration: %w", err)
	}
	if cfg.BaseURL == "" {
		return Config{}, errors.New("configuration field base_url is required")
	}
	if len(cfg.DataDirs) == 0 {
		return Config{}, errors.New("configuration field data_dirs is required")
	}
	if cfg.HTTP.ReadHeaderTimeout <= 0 || cfg.HTTP.ReadTimeout <= 0 || cfg.HTTP.WriteTimeout <= 0 || cfg.HTTP.IdleTimeout <= 0 || cfg.HTTP.ShutdownTimeout <= 0 {
		return Config{}, errors.New("configuration http duration fields must be positive")
	}
	if err := cfg.validateWorkflow(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

var workflowID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (cfg *Config) validateWorkflow() error {
	if cfg.Moderators == nil {
		return errors.New("configuration field moderators is required")
	}
	ids, emails := map[string]struct{}{}, map[string]struct{}{}
	for _, moderator := range cfg.Moderators {
		if !workflowID.MatchString(moderator.ID) {
			return errors.New("configuration field moderators.id is invalid")
		}
		address, err := mail.ParseAddress(moderator.Email)
		if err != nil || address.Name != "" || address.Address != moderator.Email {
			return errors.New("configuration field moderators.email is invalid")
		}
		normalized := strings.ToLower(address.Address)
		if _, exists := ids[moderator.ID]; exists {
			return errors.New("configuration field moderators.id is duplicated")
		}
		if _, exists := emails[normalized]; exists {
			return errors.New("configuration field moderators.email is duplicated")
		}
		ids[moderator.ID], emails[normalized] = struct{}{}, struct{}{}
	}
	if !workflowID.MatchString(cfg.Auth.KeyID) {
		return errors.New("configuration field auth.key_id is invalid")
	}
	key, err := base64.StdEncoding.DecodeString(cfg.Auth.SigningKeyText)
	if err != nil || len(key) != 32 {
		return errors.New("configuration field auth.signing_key must decode to 32 bytes")
	}
	cfg.Auth.SigningKey = key
	if cfg.SMTP.Host == "" || cfg.SMTP.Port < 1 || cfg.SMTP.Port > 65535 {
		return errors.New("configuration field smtp host or port is invalid")
	}
	from, err := mail.ParseAddress(cfg.SMTP.From)
	if err != nil || from.Name != "" || from.Address != cfg.SMTP.From {
		return errors.New("configuration field smtp.from is invalid")
	}
	if cfg.SMTP.TLSMode != "starttls" && cfg.SMTP.TLSMode != "implicit" && cfg.SMTP.TLSMode != "development_plain" {
		return errors.New("configuration field smtp.tls_mode is invalid")
	}
	if (cfg.SMTP.Username == "") != (cfg.SMTP.Password == "") {
		return errors.New("configuration fields smtp.username and smtp.password must be supplied together")
	}
	if cfg.SMTP.TLSMode == "development_plain" && (net.ParseIP(cfg.SMTP.Host) == nil || !net.ParseIP(cfg.SMTP.Host).IsLoopback() || cfg.SMTP.Username != "") {
		return errors.New("configuration field smtp.tls_mode development_plain requires loopback without authentication")
	}
	return nil
}

func validateKnownTypes(values map[string]any) error {
	baseURL, ok := values["base_url"]
	if !ok || baseURL == nil {
		return errors.New("configuration field base_url is required")
	}
	if _, ok := baseURL.(string); !ok {
		return errors.New("configuration field base_url must be a string")
	}
	dataDirs, ok := values["data_dirs"]
	if !ok || dataDirs == nil {
		return errors.New("configuration field data_dirs is required")
	}
	if _, ok := dataDirs.([]any); !ok {
		return errors.New("configuration field data_dirs must be a sequence")
	}
	httpValues, ok := values["http"]
	if !ok || httpValues == nil {
		return errors.New("configuration field http is required")
	}
	httpMap, ok := httpValues.(map[string]any)
	if !ok {
		return errors.New("configuration field http must be a mapping")
	}
	for _, key := range []string{"read_header_timeout", "read_timeout", "write_timeout", "idle_timeout", "shutdown_timeout"} {
		value, ok := httpMap[key]
		if !ok || value == nil {
			return fmt.Errorf("configuration field http.%s is required", key)
		}
		if _, ok := value.(string); !ok {
			return fmt.Errorf("configuration field http.%s must be a duration string", key)
		}
	}
	return nil
}

func parseLayer(data []byte) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err == io.EOF {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("configuration document must be a mapping")
	}
	mapping := document.Content[0]
	for index := 0; index < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, errors.New("configuration mapping keys must be strings")
		}
	}
	var values map[string]any
	if err := document.Decode(&values); err != nil {
		return nil, err
	}
	var additional yaml.Node
	if err := decoder.Decode(&additional); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("multiple documents are not supported")
	}
	return values, nil
}

func merge(dst, src map[string]any) {
	for key, value := range src {
		if child, ok := value.(map[string]any); ok {
			if existing, ok := dst[key].(map[string]any); ok {
				merge(existing, child)
				continue
			}
		}
		dst[key] = value
	}
}
