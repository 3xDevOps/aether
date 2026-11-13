// Package home is the file service of a session VM. The host serves the
// member home, and every other host file a VM mounts, to the helper's FUSE
// filesystem over vsock, by path: the guest keeps every inode, so a VM saved
// and restored keeps its open files and working directories, which virtio-fs
// cannot. docs/vm-guest.md describes it.
package home

import (
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Port is the vsock port the helper reaches the service on; Cloud
// Hypervisor relays it to the host socket <vsock socket>_1023.
const Port = 1023

// Share is one host file or directory the service exposes. With Subpath set
// it is Source/Subpath, resolved beneath Source on every request, so a
// symlink there never leads out of Source.
type Share struct {
	Source   string `json:"source"`
	Subpath  string `json:"subpath,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// Config is what one VM's service serves. Every file the VM sees is owned
// by UID and GID, and every file it creates is created as them: the guest
// never chooses a host owner.
type Config struct {
	Shares []Share `json:"shares"`
	UID    uint32  `json:"uid"`
	GID    uint32  `json:"gid"`
}

// maxPath is Linux's PATH_MAX.
const maxPath = 4096

// split parses a service path: share is -1 for the root, and rel is the
// clean relative path inside the share, "" for its root.
func split(p string, shares int) (share int, rel string, ok bool) {
	if p == "" {
		return -1, "", true
	}
	if len(p) > maxPath || strings.ContainsRune(p, 0) {
		return 0, "", false
	}
	first, rel, nested := strings.Cut(p, "/")
	i, err := strconv.Atoi(first)
	if err != nil || i < 0 || i >= shares || strconv.Itoa(i) != first {
		return 0, "", false
	}
	if nested && (rel == "." || !filepath.IsLocal(rel) || path.Clean(rel) != rel) {
		return 0, "", false
	}
	return i, rel, true
}
