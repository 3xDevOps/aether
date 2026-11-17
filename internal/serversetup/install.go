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
	SlicePath  string
	ConfigPath string
	// ConfigSkipped is set when an existing config file was left alone
	// because force was not requested.
	ConfigSkipped bool
}

// Install writes the systemd unit, the slice beside it and the config file.
// The unit and slice are always rewritten because they are package-owned; an
// existing config file is kept unless force is set because it is
// operator-owned. The written config is ServiceDefaults with values layered
// over it, so a fresh install keeps the posture the unit's ExecStart used to
// hardcode. Install never activates anything: the caller prints
// ActivateCommand instead, so an install cannot restart a running server
// behind the operator's back.
func Install(unitPath, configPath string, values map[string]string, force bool) (InstallResult, error) {
	res := InstallResult{UnitPath: unitPath, SlicePath: filepath.Join(filepath.Dir(unitPath), sliceFile), ConfigPath: configPath}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return res, fmt.Errorf("serversetup: create %s: %w", filepath.Dir(unitPath), err)
	}
	if err := os.WriteFile(unitPath, []byte(DefaultUnit()), 0o644); err != nil {
		return res, fmt.Errorf("serversetup: write %s: %w", unitPath, err)
	}
	if err := os.WriteFile(res.SlicePath, []byte(DefaultSlice()), 0o644); err != nil {
		return res, fmt.Errorf("serversetup: write %s: %w", res.SlicePath, err)
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
// directory if needed. It replaces the file by a rename, keeping its mode
// and owner, so a crash leaves the old config or the new one, never a
// torn file the server cannot start from.
func WriteConfig(path string, values map[string]string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("serversetup: create %s: %w", dir, err)
	}
	if err := replaceFile(path, []byte(Render(values))); err != nil {
		return fmt.Errorf("serversetup: write %s: %w", path, err)
	}
	return nil
}

func replaceFile(path string, data []byte) error {
	mode, uid, gid := os.FileMode(0o644), -1, -1
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		uid, gid = fileOwner(info)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil && uid >= 0 {
		err = tmp.Chown(uid, gid)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
