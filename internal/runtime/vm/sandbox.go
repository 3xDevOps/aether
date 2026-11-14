package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// hiddenDirs are what ProtectHome=yes hides from a VM's unit, even beneath
// a ReadWritePaths entry.
var hiddenDirs = []string{"/home", "/root", "/run/user"}

// fileCaps are what the file service uses to serve a home as its owner, and
// virtiofsd its shares.
var fileCaps = []string{"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER", "CAP_FSETID"}

// shareCaps are what the monitor needs to compose the virtio-fs share and
// virtiofsd its namespace sandbox.
var shareCaps = []string{"CAP_SYS_ADMIN", "CAP_SETUID", "CAP_SETGID", "CAP_SETPCAP", "CAP_MKNOD", "CAP_SETFCAP"}

// sandbox confines a VM's unit: it writes only the VM's directory and the
// host files its disks and shares name, sees no home directory, gains no
// privilege by exec, and keeps only the capabilities its processes use.
// Cloud Hypervisor makes the VM's tap, which takes CAP_NET_ADMIN.
func sandbox(dir string, rec *record, l launch) []string {
	caps := slices.Clone(fileCaps)
	if len(l.Shares) > 0 {
		caps = append(caps, shareCaps...)
	}
	if rec.Net != offline {
		caps = append(caps, "CAP_NET_ADMIN")
	}
	writable := []string{dir}
	if rec.State.Path != "" {
		writable = append(writable, rec.State.Path)
	}
	for _, c := range rec.CheckoutDisks {
		writable = append(writable, c.Disk.Path)
	}
	for _, s := range l.Shares {
		if !s.ReadOnly {
			writable = append(writable, s.Source)
		}
	}
	if l.Home != nil {
		for _, s := range l.Home.Shares {
			if !s.ReadOnly {
				writable = append(writable, s.Source)
			}
		}
	}
	props := []string{
		"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes",
		"CapabilityBoundingSet=" + strings.Join(caps, " "),
	}
	// A path that is gone is skipped rather than failing the unit, as the
	// file service reports a missing share to the guest.
	for _, p := range writable {
		props = append(props, `ReadWritePaths="-`+p+`"`)
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
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("vm runtime: %s %s: %w", what, path, err)
	}
	for _, p := range []string{abs, resolved} {
		for _, hidden := range hiddenDirs {
			if p == hidden || strings.HasPrefix(p, hidden+"/") {
				return fmt.Errorf("vm runtime: the %s %s is under %s, which every VM's unit hides (ProtectHome=yes); move it out", what, path, hidden)
			}
		}
	}
	return nil
}
