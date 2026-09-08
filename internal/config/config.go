// ABOUTME: Loads and validates layered runtime configuration for STV Poll.
// ABOUTME: Keeps deployment-owned paths and application settings explicit.
package config

import (
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"time"
)

type HTTP struct {
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
}
type Config struct {
	BaseURL  string   `yaml:"base_url"`
	DataDirs []string `yaml:"data_dirs"`
	HTTP     HTTP     `yaml:"http"`
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
		var values map[string]any
		if err := yaml.Unmarshal(data, &values); err != nil {
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
	return cfg, nil
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
