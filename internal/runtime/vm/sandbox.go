package vm

import (
	"maps"
	"os"
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
// processes write it. The disks are reached through the VM's own directory
// and each checkout's directory, so the image store never shows; the shares,
// the home and the forwarded sockets are named where they live; a forwarded
// socket by its directory, which CH reaches by following a link in the VM
// directory. The files the server trusts across boots and the base images
// the disks are built from are revealed read-only inside those writable
// directories, so a compromised unit cannot rewrite its own record, its
// launch, or a shared base: systemd binds the deeper read-only path after
// the directory.
func revealPaths(dir, kernel string, rec *record, p plan) map[string]bool {
	reveal := map[string]bool{dir: true}
	readOnly := func(path string) {
		reveal[path] = false
	}
	readOnly(filepath.Join(dir, recordFile))
	readOnly(filepath.Join(dir, launchFile))
	if rec.State.Path != "" {
		reveal[rec.State.Path] = true
	}
	for _, c := range rec.CheckoutDisks {
		reveal[filepath.Dir(c.Disk.Path)] = true
	}
	for _, pin := range backingPins(dir, rec) {
		readOnly(pin)
	}
	for _, s := range p.shares {
		reveal[s.Source] = reveal[s.Source] || !s.ReadOnly
	}
	for _, s := range p.home {
		reveal[s.Source] = reveal[s.Source] || !s.ReadOnly
	}
	for _, target := range p.links {
		d := filepath.Dir(target)
		if _, ok := reveal[d]; !ok {
			readOnly(d)
		}
	}
	if kernel != "" {
		if _, ok := reveal[kernel]; !ok {
			readOnly(kernel)
		}
	}
	return reveal
}

// backingPins are the read-only files in a VM's directory and its checkouts'
// directories that are hard links to the image store - the helper disk and
// every base a qcow2 disk is built over - which a disk shares its bytes with.
func backingPins(dir string, rec *record) []string {
	pins := []string{filepath.Join(dir, helperImage)}
	roots := []string{dir}
	for _, c := range rec.CheckoutDisks {
		roots = append(roots, filepath.Dir(c.Disk.Path))
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".base") {
				pins = append(pins, filepath.Join(root, e.Name()))
			}
		}
	}
	return pins
}

// sandbox confines a VM's unit: a tmpfs hides the whole data directory, so
// the database and every other VM's files are gone, and only the host paths
// this VM needs are bound back, writable only where it writes. The unit
// gains no privilege by exec, cannot touch the kernel, control groups, clock
// or any device but /dev/kvm and /dev/net/tun, and keeps only the
// capabilities its processes use. Cloud Hypervisor makes the VM's tap, which
// takes CAP_NET_ADMIN; without netlink it cannot touch the host's firewall
// or routes through it.
func sandbox(dataDir, monitor string, rec *record, l launch) []string {
	caps := slices.Clone(fileCaps)
	if len(l.Shares) > 0 {
		caps = append(caps, shareCaps...)
	}
	if rec.Net != offline {
		caps = append(caps, "CAP_NET_ADMIN")
	}
	props := []string{
		"NoNewPrivileges=yes", "ProtectSystem=strict",
		"ProtectKernelTunables=yes", "ProtectKernelModules=yes", "ProtectKernelLogs=yes",
		"ProtectControlGroups=yes", "ProtectClock=yes",
		"DevicePolicy=closed", "DeviceAllow=/dev/kvm rw", "DeviceAllow=/dev/net/tun rw",
		"RestrictAddressFamilies=AF_UNIX AF_INET",
		"TemporaryFileSystem=" + dataDir + ":ro",
		"CapabilityBoundingSet=" + strings.Join(caps, " "),
	}
	// ProtectHome hides the host's home directories from a monitor escape,
	// but it hides them past every bind, so it is left off when the data
	// directory, the monitor binary systemd execs, or anything the VM needs
	// lives under one (a server run from a home directory); the
	// data-directory tmpfs still hides Aether's own data either way.
	if hideHome(dataDir, monitor, l.Reveal) {
		props = append(props, "ProtectHome=yes")
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

// hideHome reports whether ProtectHome=yes is safe: no path the VM needs,
// nor the data directory a tmpfs covers, sits under a directory it hides,
// since a bind cannot reveal one that does.
func hideHome(dataDir, monitor string, reveal map[string]bool) bool {
	if underHidden(dataDir) || underHidden(monitor) {
		return false
	}
	for path := range reveal {
		if underHidden(path) {
			return false
		}
	}
	return true
}

func underHidden(path string) bool {
	for _, hidden := range hiddenDirs {
		if path == hidden || strings.HasPrefix(path, hidden+"/") {
			return true
		}
	}
	return false
}
