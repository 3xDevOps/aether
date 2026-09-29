//go:build unix

package main

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
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
