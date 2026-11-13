package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// share is one host path the monitor binds into the VM's share directory,
// at share/<index>.
type share struct {
	Source   string `json:"source"`
	Subpath  string `json:"subpath,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// firstSocketPort leaves 1024 to the helper's own API.
const firstSocketPort = 1025

type plan struct {
	shares  []share
	mounts  []guest.Mount
	sockets []guest.Socket
	// links maps each vsock.sock_<port> to the host socket Cloud Hypervisor
	// connects to for that port.
	links map[string]string
}

// specMounts lists a spec's mounts in Docker's order: the checkout, extra
// worktrees, then the rest.
func specMounts(spec runtime.Spec) []runtime.Mount {
	var out []runtime.Mount
	if spec.WorktreeHostPath != "" {
		out = append(out, runtime.Mount{HostPath: spec.WorktreeHostPath, ContainerPath: spec.WorktreeMountPath})
	}
	out = append(out, spec.ExtraWorktrees...)
	return append(out, spec.Mounts...)
}

// planMounts reads the host once per boot. A unix socket cannot cross
// virtio-fs, so sockets at the top of a shared directory are forwarded, and
// a directory holding nothing but sockets is not shared at all: the helper
// makes it on the guest's /run.
func planMounts(mounts []runtime.Mount, nestedDocker bool, vsock string) (plan, error) {
	p := plan{links: map[string]string{}}
	forward := func(host, target string) {
		port := firstSocketPort + len(p.sockets)
		p.sockets = append(p.sockets, guest.Socket{Port: uint32(port), Path: target})
		p.links[vsock+"_"+strconv.Itoa(port)] = host
	}
	for _, m := range mounts {
		if m.Subpath == "" {
			info, err := os.Stat(m.HostPath)
			if err != nil {
				return plan{}, fmt.Errorf("vm runtime: mount %s at %s: %w", m.HostPath, m.ContainerPath, err)
			}
			if info.Mode()&fs.ModeSocket != 0 {
				forward(m.HostPath, m.ContainerPath)
				continue
			}
			if info.IsDir() {
				sockets, others, err := topSockets(m.HostPath)
				if err != nil {
					return plan{}, err
				}
				for _, name := range sockets {
					forward(filepath.Join(m.HostPath, name), filepath.Join(m.ContainerPath, name))
				}
				if len(sockets) > 0 && !others {
					continue
				}
			}
		}
		p.mounts = append(p.mounts, guest.Mount{Volume: guest.VolumeShare, Source: strconv.Itoa(len(p.shares)), Target: m.ContainerPath, ReadOnly: m.ReadOnly})
		p.shares = append(p.shares, share{Source: m.HostPath, Subpath: m.Subpath, ReadOnly: m.ReadOnly})
	}
	if nestedDocker {
		p.mounts = append(p.mounts, guest.Mount{Volume: guest.VolumeData, Source: "docker", Target: "/var/lib/docker"})
	}
	return p, nil
}

func topSockets(dir string) (sockets []string, others bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, fmt.Errorf("vm runtime: read mount %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSocket != 0 {
			sockets = append(sockets, entry.Name())
		} else {
			others = true
		}
	}
	return sockets, others, nil
}

func linkSockets(dir string, links map[string]string) error {
	old, err := filepath.Glob(filepath.Join(dir, vsockSocket+"_*"))
	if err != nil {
		return err
	}
	for _, path := range old {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("vm runtime: remove socket link: %w", err)
		}
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			return fmt.Errorf("vm runtime: link socket %s: %w", target, err)
		}
	}
	return nil
}
