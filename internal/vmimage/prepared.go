package vmimage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// NewCheckoutDisk creates a qcow2 disk at path over the prepared disk
// cached under key, building that on first use: an ext4 filesystem of size
// bytes holding what fill puts in an empty directory. Concurrent first uses
// share one build, which a cancelled caller leaves running for the others:
// fill gets the build's context. A prepared disk is swept once no disk
// backs onto it.
func (s *Store) NewCheckoutDisk(ctx context.Context, key string, size int64, fill func(ctx context.Context, dir string) error, path string) error {
	if !validPreparedKey(key) {
		return fmt.Errorf("vmimage: prepared disk key %q must be letters, digits, - or _", key)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("vmimage: checkout disk: %w", err)
	}
	base := Base{ID: "prepared:" + key, Path: filepath.Join(s.preparedDir(), key, "disk.raw")}
	// Pinned under the store lock as soon as it exists, so a sweep cannot
	// remove it before the new disk backs onto it.
	pin := path + "." + dirName(base.ID) + ".base"
	for {
		s.mu.Lock()
		_, statErr := os.Stat(base.Path)
		if statErr == nil {
			err = s.pinLocked(base.Path, pin)
		}
		s.mu.Unlock()
		if statErr == nil {
			if err != nil {
				return err
			}
			_, err = s.NewRootDisk(ctx, base, path)
			return err
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("vmimage: prepared disk %s: %w", key, statErr)
		}
		built := s.builds.DoChan("prepared:"+key, func() (any, error) {
			buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), buildTimeout)
			defer cancel()
			return nil, s.buildPrepared(buildCtx, key, size, fill)
		})
		select {
		case <-ctx.Done():
			return fmt.Errorf("vmimage: prepared disk %s: %w", key, ctx.Err())
		case res := <-built:
			if res.Err != nil {
				return res.Err
			}
		}
	}
}

func (s *Store) buildPrepared(ctx context.Context, key string, size int64, fill func(ctx context.Context, dir string) error) error {
	dest := filepath.Join(s.preparedDir(), key)
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	work, err := os.MkdirTemp(s.tmpDir(), "prepared-")
	if err != nil {
		return fmt.Errorf("vmimage: prepared disk %s: %w", key, err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	stage := filepath.Join(work, "root")
	if err := os.Mkdir(stage, 0o755); err != nil {
		return err
	}
	if err := fill(ctx, stage); err != nil {
		return fmt.Errorf("vmimage: fill prepared disk %s: %w", key, err)
	}
	disk := filepath.Join(work, "disk.raw")
	if err := mkfs(ctx, disk, size, "aether-checkout", stage); err != nil {
		return err
	}
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.Chmod(disk, 0o444); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Rename(work, dest); err != nil {
		return fmt.Errorf("vmimage: store prepared disk %s: %w", key, err)
	}
	return nil
}

func validPreparedKey(key string) bool {
	if key == "" || len(key) > 200 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
