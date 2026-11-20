// Package guest is the server's side of aether-internal guest, the helper
// that runs as PID 1 in every session VM: the boot configuration the VM's
// kernel command line carries, and the client of the helper's API. The
// helper itself is package guest/helper; docs/vm-guest.md describes both.
package guest

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// APIPort is the vsock port the helper serves its API on.
	APIPort = 1024
	// GitPort is the vsock port of the helper's git service, far from the
	// host ports forwarded sockets dial, which count up from 1025.
	GitPort = 1100
	// ShareTag is the virtio-fs tag of the VM's one shared folder.
	ShareTag = "aether"
	// MainID names the session's main process, which Boot starts.
	MainID = "main"
	// SetupID names the setup script's process while Boot runs it.
	SetupID = "setup"
	// RootStep is the boot step that mounts the root disk; a boot error
	// that starts with it is the image's.
	RootStep = "switch to the root disk"
	// BrowserSocket is the development browser's control socket in a
	// session VM, which the helper serves to the host on BrowserPort.
	BrowserSocket = "/run/aether-guest/browser/control/browser.sock"
	BrowserPort   = 2001
	// SetupOutputLimit matches the Docker runtime's: how much of a failed
	// setup script's output Boot returns, from the end, where it failed.
	SetupOutputLimit = 16 << 10
	// maxCmdline is x86's COMMAND_LINE_SIZE less its terminating NUL.
	maxCmdline = 2047
)

// Volume names what a Mount's Source is relative to.
type Volume string

const (
	// VolumeShare is the virtio-fs share tagged ShareTag.
	VolumeShare Volume = "share"
	// VolumeData is the block device whose virtio serial is "data". The
	// helper creates a missing Source directory with the mode and owner of
	// its Target in the image.
	VolumeData Volume = "data"
	// VolumeHome is the host's file service (package guest/home), whose
	// root lists one entry per shared host file; Source is the entry's
	// index.
	VolumeHome Volume = "home"
	// VolumeCache is the block device whose virtio serial is "cache", the
	// session's copy of the member's tool caches; Source is ".".
	VolumeCache Volume = "cache"
	// VolumeHelper is the helper's own executable, the server binary the VM
	// booted with; Source is ".".
	VolumeHelper Volume = "helper"
	// VolumeState is the block device whose virtio serial is "state", a disk
	// the session keeps across its VMs. Like VolumeData, a missing Source
	// directory is created with the mode and owner of its Target.
	VolumeState Volume = "state"
	// VolumeProgram is the read-only block device whose virtio serial is
	// "program-<Source>", an agent program's disk, which holds the program
	// as ProgramEntry. The helper binds it over the one at Target, and
	// leaves a Target that is missing, or of another type, to the home.
	VolumeProgram Volume = "program"
)

// ProgramEntry names the program on its disk.
const ProgramEntry = "program"

// Config is what the helper needs before it can serve: everything on it is
// readable by any process in the VM through /proc/cmdline, so it must not
// hold secrets.
type Config struct {
	Hostname string
	Network  *Network
	// Mounts are bound in order, so a parent comes before its children.
	Mounts []Mount
	// Checkouts are mounted after Mounts.
	Checkouts []CheckoutMount
	Sockets   []Socket
	Serves    []Socket
	Loopbacks []Loopback
	// MemoryLimit caps the session's memory, in bytes; Swap sizes the swap
	// file the helper makes on the data disk. 0 leaves either out.
	MemoryLimit uint64
	Swap        uint64
}

// Network configures the VM's one network interface.
type Network struct {
	Address netip.Prefix
	Gateway netip.Addr
	// DNS, when set, replaces the image's /etc/resolv.conf, which then
	// searches Search.
	DNS    []netip.Addr
	Search []string
}

// Mount binds Source, a relative path in Volume ("." for its root), at
// Target. Share and home mounts are nosuid and nodev. Overlay, for a home
// mount, keeps the guest's writes in a layer on the data disk instead.
type Mount struct {
	Volume   Volume
	Source   string
	Target   string
	ReadOnly bool
	Overlay  bool
	// Seed, for a state mount, names files under Target that the helper
	// copies into Source at every boot, from what Target shows before this
	// mount covers it; a file missing there is removed from Source.
	Seed []string
}

// CheckoutMount mounts a session checkout's disk, the block device whose
// virtio serial is Serial, for the git service under Key and binds it at
// Target.
type CheckoutMount struct {
	Serial string
	Key    string
	Target string
}

// Socket makes the helper listen on Path in the VM and relay every
// connection to vsock port Port on the host. As one of Config.Serves it is
// the other way round: the helper listens on vsock port Port for the host
// and relays each connection to the socket at Path in the VM.
type Socket struct {
	Port uint32
	Path string
}

// Loopback makes the helper listen on 127.0.0.1:Port in the VM and relay
// each connection to the socket at Path in the VM, for a program that
// reaches a server only by an HTTP address.
type Loopback struct {
	Port uint16
	Path string
}

// KernelArgs returns the VM's kernel command line: base, then the
// configuration, then the arguments that start the helper, which must come
// last.
func (c Config) KernelArgs(base string) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	args := []string{strings.TrimSpace(base)}
	if c.Hostname != "" {
		args = append(args, "aether.hostname="+c.Hostname)
	}
	if n := c.Network; n != nil {
		args = append(args, "aether.net="+n.Address.String()+","+n.Gateway.String())
		if len(n.DNS) > 0 {
			dns := make([]string, len(n.DNS))
			for i, a := range n.DNS {
				dns[i] = a.String()
			}
			args = append(args, "aether.dns="+strings.Join(dns, ","))
		}
		if len(n.Search) > 0 {
			args = append(args, "aether.search="+strings.Join(n.Search, ","))
		}
	}
	for _, m := range c.Mounts {
		arg := "aether.mount=" + string(m.Volume) + "," + escapePath(m.Source) + "," + escapePath(m.Target)
		switch {
		case m.ReadOnly:
			arg += ",ro"
		case m.Overlay:
			arg += ",overlay"
		}
		args = append(args, arg)
		for _, f := range m.Seed {
			args = append(args, "aether.seed="+escapePath(m.Target)+","+escapePath(f))
		}
	}
	for _, m := range c.Checkouts {
		args = append(args, "aether.checkout="+m.Serial+","+m.Key+","+escapePath(m.Target))
	}
	for _, s := range c.Sockets {
		args = append(args, "aether.sock="+strconv.FormatUint(uint64(s.Port), 10)+","+escapePath(s.Path))
	}
	for _, s := range c.Serves {
		args = append(args, "aether.serve="+strconv.FormatUint(uint64(s.Port), 10)+","+escapePath(s.Path))
	}
	for _, l := range c.Loopbacks {
		args = append(args, "aether.loopback="+strconv.FormatUint(uint64(l.Port), 10)+","+escapePath(l.Path))
	}
	if c.MemoryLimit > 0 || c.Swap > 0 {
		args = append(args, "aether.memory="+strconv.FormatUint(c.MemoryLimit, 10)+","+strconv.FormatUint(c.Swap, 10))
	}
	line := strings.TrimSpace(strings.Join(append(args, "--", "guest"), " "))
	if len(line) > maxCmdline {
		return "", fmt.Errorf("guest: kernel command line is %d bytes, over the %d-byte limit: %w", len(line), maxCmdline, ErrCmdlineTooLong)
	}
	return line, nil
}

// ErrCmdlineTooLong is KernelArgs' error for a configuration the kernel's
// command line cannot hold.
var ErrCmdlineTooLong = errors.New("the configuration does not fit the kernel command line")

// ParseCmdline reads the configuration KernelArgs wrote from a kernel
// command line. Arguments without the aether. prefix are ignored.
func ParseCmdline(cmdline string) (Config, error) {
	var c Config
	for _, arg := range strings.Fields(cmdline) {
		if arg == "--" {
			break
		}
		key, value, ok := strings.Cut(arg, "=")
		if !ok || !strings.HasPrefix(key, "aether.") {
			continue
		}
		if err := c.parseArg(key, value); err != nil {
			return Config{}, fmt.Errorf("guest: kernel argument %s: %w", key, err)
		}
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c *Config) parseArg(key, value string) error {
	fields := strings.Split(value, ",")
	switch key {
	case "aether.hostname":
		c.Hostname = value
	case "aether.net":
		if len(fields) != 2 {
			return errors.New("want <address/prefix>,<gateway>")
		}
		address, err := netip.ParsePrefix(fields[0])
		if err != nil {
			return err
		}
		gateway, err := netip.ParseAddr(fields[1])
		if err != nil {
			return err
		}
		c.network().Address, c.network().Gateway = address, gateway
	case "aether.dns":
		dns := make([]netip.Addr, 0, len(fields))
		for _, f := range fields {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return err
			}
			dns = append(dns, a)
		}
		c.network().DNS = dns
	case "aether.search":
		c.network().Search = fields
	case "aether.mount":
		if len(fields) != 3 && (len(fields) != 4 || (fields[3] != "ro" && fields[3] != "overlay")) {
			return errors.New("want <volume>,<source>,<target>[,ro|,overlay]")
		}
		source, err := url.PathUnescape(fields[1])
		if err != nil {
			return err
		}
		target, err := url.PathUnescape(fields[2])
		if err != nil {
			return err
		}
		option := ""
		if len(fields) == 4 {
			option = fields[3]
		}
		c.Mounts = append(c.Mounts, Mount{Volume: Volume(fields[0]), Source: source, Target: target, ReadOnly: option == "ro", Overlay: option == "overlay"})
	case "aether.seed":
		if len(fields) != 2 {
			return errors.New("want <target>,<file>")
		}
		target, err := url.PathUnescape(fields[0])
		if err != nil {
			return err
		}
		file, err := url.PathUnescape(fields[1])
		if err != nil {
			return err
		}
		i := len(c.Mounts) - 1
		if i < 0 || c.Mounts[i].Target != target {
			return errors.New("must follow the mount it seeds")
		}
		c.Mounts[i].Seed = append(c.Mounts[i].Seed, file)
	case "aether.checkout":
		if len(fields) != 3 {
			return errors.New("want <serial>,<key>,<target>")
		}
		target, err := url.PathUnescape(fields[2])
		if err != nil {
			return err
		}
		c.Checkouts = append(c.Checkouts, CheckoutMount{Serial: fields[0], Key: fields[1], Target: target})
	case "aether.sock", "aether.serve":
		if len(fields) != 2 {
			return errors.New("want <port>,<path>")
		}
		port, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			return err
		}
		p, err := url.PathUnescape(fields[1])
		if err != nil {
			return err
		}
		switch key {
		case "aether.sock":
			c.Sockets = append(c.Sockets, Socket{Port: uint32(port), Path: p})
		case "aether.serve":
			c.Serves = append(c.Serves, Socket{Port: uint32(port), Path: p})
		}
	case "aether.loopback":
		if len(fields) != 2 {
			return errors.New("want <port>,<path>")
		}
		port, err := strconv.ParseUint(fields[0], 10, 16)
		if err != nil {
			return err
		}
		p, err := url.PathUnescape(fields[1])
		if err != nil {
			return err
		}
		c.Loopbacks = append(c.Loopbacks, Loopback{Port: uint16(port), Path: p})
	case "aether.memory":
		if len(fields) != 2 {
			return errors.New("want <limit>,<swap>")
		}
		limit, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return err
		}
		swap, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return err
		}
		c.MemoryLimit, c.Swap = limit, swap
	default:
		return errors.New("unknown argument")
	}
	return nil
}

func (c *Config) network() *Network {
	if c.Network == nil {
		c.Network = &Network{}
	}
	return c.Network
}

// Validate reports every problem that would make the helper refuse c.
func (c Config) Validate() error {
	var errs []error
	if c.Hostname != "" && !validHostname(c.Hostname) {
		errs = append(errs, fmt.Errorf("hostname %q is not a valid host name", c.Hostname))
	}
	if n := c.Network; n != nil {
		if !n.Address.IsValid() || !n.Address.Addr().Is4() || !n.Gateway.Is4() {
			errs = append(errs, errors.New("network needs an IPv4 address with a prefix and an IPv4 gateway"))
		}
		for _, a := range n.DNS {
			if !a.IsValid() {
				errs = append(errs, errors.New("network has an invalid DNS server"))
			}
		}
		for _, d := range n.Search {
			if !ValidDomain(d) {
				errs = append(errs, fmt.Errorf("search domain %q is not a valid domain name", d))
			}
		}
	}
	for i, m := range c.Mounts {
		switch m.Volume {
		case VolumeShare, VolumeData, VolumeState:
		case VolumeHome, VolumeProgram:
			if n, err := strconv.Atoi(m.Source); err != nil || n < 0 || strconv.Itoa(n) != m.Source {
				errs = append(errs, fmt.Errorf("mount %d: %s source %q must be an index", i, m.Volume, m.Source))
			}
		case VolumeCache, VolumeHelper:
			if m.Source != "." {
				errs = append(errs, fmt.Errorf("mount %d: %s source must be \".\"", i, m.Volume))
			}
		default:
			errs = append(errs, fmt.Errorf("mount %d: unknown volume %q", i, m.Volume))
		}
		if len(m.Seed) > 0 && m.Volume != VolumeState {
			errs = append(errs, fmt.Errorf("mount %d: only a state mount can be seeded", i))
		}
		for _, f := range m.Seed {
			if !filepath.IsLocal(f) || path.Clean(f) != f {
				errs = append(errs, fmt.Errorf("mount %d: seed %q must be a clean relative path", i, f))
			}
		}
		if m.Overlay && (m.Volume != VolumeHome || m.ReadOnly) {
			errs = append(errs, fmt.Errorf("mount %d: only a writable home mount can be an overlay", i))
		}
		if !filepath.IsLocal(m.Source) || path.Clean(m.Source) != m.Source {
			errs = append(errs, fmt.Errorf("mount %d: source %q must be a clean relative path", i, m.Source))
		}
		if !cleanAbs(m.Target) || m.Target == "/" {
			errs = append(errs, fmt.Errorf("mount %d: target %q must be a clean absolute path below /", i, m.Target))
		}
	}
	for i, m := range c.Checkouts {
		if !ValidCheckoutKey(m.Serial) || len(m.Serial) > 20 {
			errs = append(errs, fmt.Errorf("checkout %d: serial %q must be at most 20 letters, digits, - or _", i, m.Serial))
		}
		if !ValidCheckoutKey(m.Key) {
			errs = append(errs, fmt.Errorf("checkout %d: key %q must be letters, digits, - or _", i, m.Key))
		}
		if !cleanAbs(m.Target) || m.Target == "/" {
			errs = append(errs, fmt.Errorf("checkout %d: target %q must be a clean absolute path below /", i, m.Target))
		}
	}
	for _, set := range []struct {
		kind    string
		sockets []Socket
	}{{"socket", c.Sockets}, {"served socket", c.Serves}} {
		kind := set.kind
		for i, s := range set.sockets {
			if s.Port == 0 || s.Port == APIPort {
				errs = append(errs, fmt.Errorf("%s %d: port %d is reserved", kind, i, s.Port))
			}
			if !cleanAbs(s.Path) || s.Path == "/" {
				errs = append(errs, fmt.Errorf("%s %d: path %q must be a clean absolute path below /", kind, i, s.Path))
			}
		}
	}
	for i, l := range c.Loopbacks {
		if l.Port == 0 {
			errs = append(errs, fmt.Errorf("loopback %d: port 0 is reserved", i))
		}
		if !cleanAbs(l.Path) || l.Path == "/" {
			errs = append(errs, fmt.Errorf("loopback %d: path %q must be a clean absolute path below /", i, l.Path))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("guest: invalid configuration: %w", errors.Join(errs...))
	}
	return nil
}

// ValidCheckoutKey reports whether key can name a checkout in the VM: one
// path element of at most 64 letters, digits, - or _.
func ValidCheckoutKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func cleanAbs(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsRune(p, 0)
}

func validHostname(name string) bool {
	return len(name) <= 64 && ValidDomain(name)
}

// ValidDomain reports whether name is a domain name of letters, digits and
// hyphens, without a trailing dot.
func ValidDomain(name string) bool {
	if len(name) > 253 {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// escapePath keeps a path readable on the command line: only bytes that
// would split an argument or a field are escaped.
func escapePath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}
