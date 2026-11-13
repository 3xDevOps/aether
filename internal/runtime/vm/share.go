package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/home"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// share is one host path the monitor binds into the VM's virtio-fs share
// directory, at share/<index>.
type share = home.Share

// firstSocketPort leaves 1024 to the helper's own API.
const firstSocketPort = 1025

type plan struct {
	// shares go over virtio-fs: only the checkouts, until they move onto
	// the VM's own disk.
	shares []share
	// home is every other host path, served by the file service.
	home    []home.Share
	mounts  []guest.Mount
	sockets []guest.Socket
	// links maps each vsock.sock_<port> to the host socket Cloud Hypervisor
	// connects to for that port.
	links map[string]string
}

// planMounts reads the host once per boot. Checkouts go over virtio-fs;
// scratch space and caches live on the VM's own disks; the server binary
// is the helper's own; every other host path goes through the file
// service, so it survives a save and restore. A unix socket cannot cross
// either, so sockets at the top of a shared directory are forwarded, and a
// directory holding nothing but sockets is not shared at all: the helper
// makes it on the guest's /run. Only a directory only root can write is
// trusted: Cloud Hypervisor connects by path as root, so a socket a guest
// could replace with a symlink would reach any host socket.
func planMounts(checkouts, mounts []runtime.Mount, nestedDocker bool, dir string, trusted func(dir string) bool) (plan, error) {
	p := plan{links: map[string]string{}}
	vsock := filepath.Join(dir, vsockSocket)
	forward := func(host, target string) {
		port := firstSocketPort + len(p.sockets)
		p.sockets = append(p.sockets, guest.Socket{Port: uint32(port), Path: target})
		p.links[vsock+"_"+strconv.Itoa(port)] = host
	}
	for _, m := range checkouts {
		p.mounts = append(p.mounts, guest.Mount{Volume: guest.VolumeShare, Source: strconv.Itoa(len(p.shares)), Target: m.ContainerPath, ReadOnly: m.ReadOnly})
		p.shares = append(p.shares, share{Source: m.HostPath, Subpath: m.Subpath, ReadOnly: m.ReadOnly})
	}
	for _, m := range mounts {
		switch m.Kind {
		case runtime.MountScratch:
			p.mounts = append(p.mounts, guest.Mount{Volume: guest.VolumeData, Source: strings.TrimPrefix(m.ContainerPath, "/"), Target: m.ContainerPath})
			continue
		case runtime.MountCache:
			p.mounts = append(p.mounts, guest.Mount{Volume: guest.VolumeCache, Source: ".", Target: m.ContainerPath})
			continue
		case runtime.MountServerBinary:
			p.mounts = append(p.mounts, guest.Mount{Volume: guest.VolumeHelper, Source: ".", Target: m.ContainerPath, ReadOnly: true})
			continue
		}
		if m.Subpath == "" {
			info, err := os.Stat(m.HostPath)
			if err != nil {
				return plan{}, fmt.Errorf("vm runtime: mount %s at %s: %w", m.HostPath, m.ContainerPath, err)
			}
			if info.Mode()&fs.ModeSocket != 0 && trusted(filepath.Dir(m.HostPath)) {
				forward(m.HostPath, m.ContainerPath)
				continue
			}
			if info.IsDir() && trusted(m.HostPath) {
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
		overlay := m.Overlay != ""
		p.mounts = append(p.mounts, guest.Mount{Volume: guest.VolumeHome, Source: strconv.Itoa(len(p.home)), Target: m.ContainerPath, ReadOnly: m.ReadOnly, Overlay: overlay})
		p.home = append(p.home, home.Share{Source: m.HostPath, Subpath: m.Subpath, ReadOnly: m.ReadOnly || overlay})
	}
	if len(p.home) > 0 {
		p.links[vsock+"_"+strconv.Itoa(home.Port)] = filepath.Join(dir, homeSocket)
	}
	if nestedDocker {
		p.mounts = append(p.mounts, guest.Mount{Volume: guest.VolumeData, Source: "docker", Target: "/var/lib/docker"})
	}
	return p, nil
}

func rootOnly(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && info.Mode().Perm()&0o022 == 0
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
