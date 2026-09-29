//go:build unix

package main

import (
	"fmt"
	"io/fs"
	"os"
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
// one that belongs to another user or that this user cannot write. The
// edge's files are private to whoever creates them, so a directory shared
// by two users ends with files one of them cannot open.
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
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != uint32(uid) {
		return fmt.Errorf("data directory %s belongs to uid %d and aether-edge runs as uid %d; run chown -R %d %s, or run the edge as uid %d",
			dir, st.Uid, uid, uid, dir, st.Uid)
	}
	if err := unix.Access(dir, unix.W_OK|unix.X_OK); err != nil {
		return fmt.Errorf("data directory %s is not writable by uid %d, the user aether-edge runs as: %w; give it write access, or name another with --data or AETHER_EDGE_DATA",
			dir, uid, err)
	}
	return nil
}
