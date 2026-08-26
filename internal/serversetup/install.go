package serversetup

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
)

// InstallResult reports what Install put on disk.
type InstallResult struct {
	UnitPath   string
	ConfigPath string
	// ConfigSkipped is set when an existing config file was left alone
	// because force was not requested.
	ConfigSkipped bool
}

// Install writes the systemd unit and the config file. The unit is always
// rewritten because it is package-owned; an existing config file is kept
// unless force is set because it is operator-owned. The written config is
// ServiceDefaults with values layered over it, so a fresh install keeps the
// posture the unit's ExecStart used to hardcode. Install never activates
// anything: the caller prints ActivateCommand instead, so an install cannot
// restart a running server behind the operator's back.
func Install(unitPath, configPath string, values map[string]string, force bool) (InstallResult, error) {
	res := InstallResult{UnitPath: unitPath, ConfigPath: configPath}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return res, fmt.Errorf("serversetup: create %s: %w", filepath.Dir(unitPath), err)
	}
	if err := os.WriteFile(unitPath, []byte(DefaultUnit()), 0o644); err != nil {
		return res, fmt.Errorf("serversetup: write %s: %w", unitPath, err)
	}
	if _, err := os.Stat(configPath); err == nil {
		if !force {
			res.ConfigSkipped = true
			return res, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, fmt.Errorf("serversetup: stat %s: %w", configPath, err)
	}
	config := ServiceDefaults()
	maps.Copy(config, values)
	if err := WriteConfig(configPath, config); err != nil {
		return res, err
	}
	return res, nil
}

// WriteConfig renders values into the config file at path, creating its
// directory if needed.
func WriteConfig(path string, values map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("serversetup: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(Render(values)), 0o644); err != nil {
		return fmt.Errorf("serversetup: write %s: %w", path, err)
	}
	return nil
}
