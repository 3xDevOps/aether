package memberhome

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// MigrateLegacyHomes flattens known harness homes into each member's shared
// home. Each entry is moved individually so existing destinations survive a
// migration conflict.
func MigrateLegacyHomes(root string, harnessNames []string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("memberhome: migration root is required")
	}
	root = filepath.Clean(root)
	members, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memberhome: read migration root %q: %w", root, err)
	}
	harnesses := make(map[string]struct{}, len(harnessNames))
	for _, name := range harnessNames {
		harnesses[name] = struct{}{}
	}
	for _, member := range members {
		if !member.IsDir() {
			continue
		}
		memberPath := filepath.Join(root, member.Name())
		legacyEntries, err := os.ReadDir(memberPath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("memberhome: read member home %q: %w", memberPath, err)
		}
		for _, legacy := range legacyEntries {
			if !legacy.IsDir() {
				continue
			}
			if _, ok := harnesses[legacy.Name()]; !ok {
				continue
			}
			if err := migrateHarness(filepath.Join(memberPath, legacy.Name()), memberPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrateHarness(legacyPath, memberPath string) error {
	entries, err := os.ReadDir(legacyPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memberhome: read legacy home %q: %w", legacyPath, err)
	}
	for _, entry := range entries {
		source := filepath.Join(legacyPath, entry.Name())
		destination := filepath.Join(memberPath, entry.Name())
		if _, statErr := os.Lstat(destination); statErr == nil {
			warnMigrationConflict(source, destination)
			continue
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("memberhome: inspect migration destination %q: %w", destination, statErr)
		}
		if err := os.Rename(source, destination); err != nil {
			if isRenameConflict(err) {
				warnMigrationConflict(source, destination)
				continue
			}
			return fmt.Errorf("memberhome: move %q to %q: %w", source, destination, err)
		}
	}
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if isRenameConflict(err) {
			return nil
		}
		return fmt.Errorf("memberhome: remove empty legacy home %q: %w", legacyPath, err)
	}
	return nil
}

func warnMigrationConflict(source, destination string) {
	slog.Warn("memberhome: keeping existing migration destination", "source", source, "destination", destination)
}

func isRenameConflict(err error) bool {
	return os.IsExist(err) || errors.Is(err, syscall.EEXIST) || errors.Is(err, syscall.ENOTEMPTY)
}
