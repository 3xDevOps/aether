// Package cli is the aether command's SSH client: it dials the linked
// server, opens the control and attach/setup subsystems, and stores the
// local linked-server config.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the local linked-server configuration written by `aether link`.
type Config struct {
	Addr       string `json:"addr"`
	User       string `json:"user,omitempty"`
	Key        string `json:"key,omitempty"`
	Repo       string `json:"repo,omitempty"`
	KnownHosts string `json:"known_hosts,omitempty"`
}

func (c Config) user() string {
	if c.User != "" {
		return c.User
	}
	return "aether"
}

func (c Config) keyPath() string {
	if c.Key != "" {
		return c.Key
	}
	return defaultPath(".ssh", "id_ed25519")
}

func (c Config) knownHostsPath() string {
	if c.KnownHosts != "" {
		return c.KnownHosts
	}
	return defaultPath(".ssh", "known_hosts")
}

// Path is the linked-server config file (~/.config/aether/config.json).
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cli: config dir: %w", err)
	}
	return filepath.Join(dir, "aether", "config.json"), nil
}

// Load reads the linked-server config. Missing files surface as a
// "not linked" error.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("not linked; run aether link <addr>")
		}
		return Config{}, fmt.Errorf("cli: read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		return Config{}, fmt.Errorf("cli: parse config: %w", err)
	}
	if cfg.Addr == "" {
		return Config{}, fmt.Errorf("cli: config %s has no addr", path)
	}
	return cfg, nil
}

// Save writes cfg to the linked-server config path (directory 0700, file 0600).
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cli: create config dir: %w", err)
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("cli: write config: %w", err)
	}
	return nil
}

func defaultPath(elems ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{home}, elems...)...)
}
