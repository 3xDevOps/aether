package vmimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HelperDisk links path to a read-only ext4 disk holding binary as /init,
// with empty /dev, /proc, /sys and /mnt for the helper to mount on. The disk
// is built once per binary digest. Booting it as the kernel's root keeps the
// helper's pages reclaimable, which an initramfs would not.
func (s *Store) HelperDisk(ctx context.Context, binary, path string) (Disk, error) {
	sum, err := fileDigest(binary)
	if err != nil {
		return Disk{}, err
	}
	// Building under the lock keeps a sweep from removing the disk before it
	// is linked; it happens once per helper binary.
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.helpersDir(), sum)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err = s.buildHelper(ctx, binary, dir); err != nil {
			return Disk{}, fmt.Errorf("vmimage: helper disk for %s: %w", binary, err)
		}
	} else if err != nil {
		return Disk{}, fmt.Errorf("vmimage: helper disk for %s: %w", binary, err)
	}
	if err := s.pinLocked(filepath.Join(dir, "helper.raw"), path); err != nil {
		return Disk{}, err
	}
	return Disk{Path: path, Format: FormatRaw}, nil
}

func (s *Store) buildHelper(ctx context.Context, binary, dir string) error {
	work, err := os.MkdirTemp(s.tmpDir(), "helper-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	stage := filepath.Join(work, "root")
	for _, d := range []string{"", "dev", "proc", "sys", "mnt"} {
		if err = os.Mkdir(filepath.Join(stage, d), 0o755); err != nil {
			return err
		}
	}
	size, err := copyFile(binary, filepath.Join(stage, "init"))
	if err != nil {
		return err
	}
	disk := filepath.Join(work, "helper.raw")
	if err := mkfs(ctx, disk, (size+16<<20)&^(1<<20-1), "aether-helper", stage, "-O", "^has_journal"); err != nil {
		return err
	}
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.Chmod(disk, 0o444); err != nil {
		return err
	}
	return os.Rename(work, dir)
}

// mkfs makes an ext4 filesystem of size bytes in a new sparse file at path,
// filled from srcDir when it is not empty. mkfs.ext4 marks the inode tables
// of a regular file zeroed, so the guest never writes them out.
func mkfs(ctx context.Context, path string, size int64, label, srcDir string, extra ...string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("vmimage: create %s: %w", path, err)
	}
	err = f.Truncate(size)
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("vmimage: size %s: %w", path, err)
	}
	args := []string{"-q", "-F", "-t", "ext4", "-L", label}
	if srcDir != "" {
		args = append(args, "-d", srcDir)
	}
	args = append(append(args, extra...), path)
	return run(ctx, "mkfs.ext4", args...)
}

// copyDisk copies the disk image src to a new file dst as bytes, sharing
// blocks where the filesystem can and keeping holes. dst is writable even
// when src, a base, is not.
func copyDisk(ctx context.Context, src, dst string) error {
	if err := run(ctx, "cp", "--reflink=auto", "--sparse=always", "--", src, dst); err != nil {
		return err
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return fmt.Errorf("vmimage: copy %s: %w", src, err)
	}
	return nil
}

func run(ctx context.Context, name string, args ...string) error {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vmimage: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("vmimage: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("vmimage: read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	return n, errors.Join(err, out.Close())
}
