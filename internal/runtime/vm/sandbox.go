package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// hiddenDirs are what ProtectHome=yes hides from a VM's unit, even beneath
// a bind that reveals a path.
var hiddenDirs = []string{"/home", "/root", "/run/user"}

// fileCaps are what the file service uses to serve a home as its owner, and
// virtiofsd its shares.
var fileCaps = []string{"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER", "CAP_FSETID"}

// shareCaps are what the monitor needs to compose the virtio-fs share and
// virtiofsd its namespace sandbox. The monitor drops them once virtiofsd
// runs (dropShareCaps).
var shareCaps = []string{"CAP_SYS_ADMIN", "CAP_SETUID", "CAP_SETGID", "CAP_SETPCAP", "CAP_MKNOD", "CAP_SETFCAP"}

// revealPaths lists each host path a VM's unit needs, by whether its
// processes write it. The disks and their backing links are reached through
// the VM's own directory and each checkout's directory, so the image store
// never shows; the shares, the home and the forwarded sockets are named
// where they live; a forwarded socket by its directory, which CH reaches
// by following a link in the VM directory.
func revealPaths(dir, kernel string, rec *record, p plan) map[string]bool {
	reveal := map[string]bool{dir: true}
	if rec.State.Path != "" {
		reveal[rec.State.Path] = true
	}
	for _, c := range rec.CheckoutDisks {
		reveal[filepath.Dir(c.Disk.Path)] = true
	}
	for _, s := range p.shares {
		reveal[s.Source] = reveal[s.Source] || !s.ReadOnly
	}
	for _, s := range p.home {
		reveal[s.Source] = reveal[s.Source] || !s.ReadOnly
	}
	for _, target := range p.links {
		reveal[filepath.Dir(target)] = true
	}
	if kernel != "" {
		if _, ok := reveal[kernel]; !ok {
			reveal[kernel] = false
		}
	}
	return reveal
}

// sandbox confines a VM's unit: a tmpfs hides the whole data directory, so
// the database and every other VM's files are gone, and only the host paths
// this VM needs are bound back, writable only where it writes. The unit
// sees no home directory, gains no privilege by exec, cannot touch the
// kernel, control groups, clock or any device but /dev/kvm and /dev/net/tun,
// and keeps only the capabilities its processes use. Cloud Hypervisor makes
// the VM's tap, which takes CAP_NET_ADMIN; without netlink it cannot touch
// the host's firewall or routes through it.
func sandbox(dataDir string, rec *record, l launch) []string {
	caps := slices.Clone(fileCaps)
	if len(l.Shares) > 0 {
		caps = append(caps, shareCaps...)
	}
	if rec.Net != offline {
		caps = append(caps, "CAP_NET_ADMIN")
	}
	props := []string{
		"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes",
		"ProtectKernelTunables=yes", "ProtectKernelModules=yes", "ProtectKernelLogs=yes",
		"ProtectControlGroups=yes", "ProtectClock=yes",
		"DevicePolicy=closed", "DeviceAllow=/dev/kvm rw", "DeviceAllow=/dev/net/tun rw",
		"RestrictAddressFamilies=AF_UNIX AF_INET",
		"TemporaryFileSystem=" + dataDir + ":ro",
		"CapabilityBoundingSet=" + strings.Join(caps, " "),
	}
	// A path that is gone is skipped rather than failing the unit, as the
	// file service reports a missing share to the guest. Sorted so the unit
	// is the same across boots.
	for _, path := range slices.Sorted(maps.Keys(l.Reveal)) {
		key := "BindReadOnlyPaths"
		if l.Reveal[path] {
			key = "BindPaths"
		}
		props = append(props, key+`="-`+path+`"`)
	}
	return props
}

// checkVisible fails for a path the VM units need that ProtectHome hides,
// as spelled or where its links lead.
func checkVisible(what, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("vm runtime: %s %s: %w", what, path, err)
	}
	if err = visible(what, path, abs); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("vm runtime: %s %s: %w", what, path, err)
	}
	return visible(what, path, resolved)
}

func visible(what, path, p string) error {
	for _, hidden := range hiddenDirs {
		if p == hidden || strings.HasPrefix(p, hidden+"/") {
			return fmt.Errorf("vm runtime: the %s %s is under %s, which every VM's unit hides (ProtectHome=yes); move it out", what, path, hidden)
		}
	}
	return nil
}
