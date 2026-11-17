package serversetup

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
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
// directory if needed.
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

// replaceFile writes data to path, or to the file a symlink at path names,
// by a rename beside it that keeps the file's mode and group, so a crash
// leaves the old content or the new, never a torn file. It writes in place
// where the writer does not own the file or cannot give a new file its
// group, where the file is not a regular one, or where the directory takes
// no new file. Root renames only onto a
// file root owns: the path is resolved again for the rename, and whoever
// owns the file may have swapped a directory on it since.
func replaceFile(path string, data []byte) error {
	target, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.WriteFile(path, data, 0o644)
	}
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	uid, gid := fileOwner(info)
	if !info.Mode().IsRegular() || (uid >= 0 && uid != os.Geteuid()) {
		return os.WriteFile(target, data, mode)
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+"-*")
	if errors.Is(err, os.ErrPermission) {
		return os.WriteFile(target, data, mode)
	}
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	if uid >= 0 && tmp.Chown(-1, gid) != nil {
		_ = tmp.Close()
		return os.WriteFile(target, data, mode)
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), target)
	}
	if err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir makes the rename in dir survive a power loss. Windows cannot
// open a directory to flush it.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
