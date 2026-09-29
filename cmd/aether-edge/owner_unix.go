//go:build unix

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func checkOwner(path string, info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := os.Geteuid(); uint32(uid) != st.Uid {
		return fmt.Errorf("%s belongs to uid %d and this command runs as uid %d; run it as the edge's user, as in sudo -u aether-edge aether-edge servers list",
			path, st.Uid, uid)
	}
	return nil
}

// checkDataDir creates the data directory when it is missing, and refuses
// one this user cannot write, one other users can enter, or one whose
// signing key or database another user owns. The directory may belong to
// another user when this one writes it through its group, as a container
// platform arranges; the files in it are 0600, so only their owner opens
// them.
func checkDataDir(dir string) error {
	uid := os.Geteuid()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("data directory: %w; create it for uid %d, the user aether-edge runs as, or name another with --data or AETHER_EDGE_DATA",
			err, uid)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("data directory: %w", err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if err := unix.Access(dir, unix.W_OK|unix.X_OK); err != nil {
		return fmt.Errorf("data directory %s (uid %d, gid %d, mode %04o) is not writable by uid %d, the user aether-edge runs as: %w; give that user write access as the directory's owner or through its group, or name another with --data or AETHER_EDGE_DATA",
			dir, st.Uid, st.Gid, info.Mode().Perm(), uid, err)
	}
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		return fmt.Errorf("data directory %s has mode %04o, which lets every user on this machine into the directory of the edge's signing key; run chmod o-rwx %s",
			dir, perm, dir)
	}
	for _, name := range []string{"edge_key", "edge.db"} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("data directory: %w", err)
		}
		if owner := info.Sys().(*syscall.Stat_t).Uid; owner != uint32(uid) {
			return fmt.Errorf("%s belongs to uid %d and aether-edge runs as uid %d; the edge opens only files it created, so run chown -R %d %s",
				path, owner, uid, uid, dir)
		}
	}
	return nil
}
