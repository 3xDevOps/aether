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
	// ShareTag is the virtio-fs tag of the VM's one shared folder.
	ShareTag = "aether"
	// MainID names the session's main process, which Boot starts.
	MainID = "main"
	// SetupID names the setup script's process while Boot runs it.
	SetupID = "setup"
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
)

// Config is what the helper needs before it can serve: everything on it is
// readable by any process in the VM through /proc/cmdline, so it must not
// hold secrets.
type Config struct {
	Hostname string
	Network  *Network
	// Mounts are bound in order, so a parent comes before its children.
	Mounts  []Mount
	Sockets []Socket
}

// Network configures the VM's one network interface.
type Network struct {
	Address netip.Prefix
	Gateway netip.Addr
	// DNS, when set, replaces the image's /etc/resolv.conf.
	DNS []netip.Addr
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
}

// Socket makes the helper listen on Path in the VM and relay every
// connection to vsock port Port on the host.
type Socket struct {
	Port uint32
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
	}
	for _, s := range c.Sockets {
		args = append(args, "aether.sock="+strconv.FormatUint(uint64(s.Port), 10)+","+escapePath(s.Path))
	}
	line := strings.TrimSpace(strings.Join(append(args, "--", "guest"), " "))
	if len(line) > maxCmdline {
		return "", fmt.Errorf("guest: kernel command line is %d bytes, over the %d-byte limit", len(line), maxCmdline)
	}
	return line, nil
}

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
		c.Network = &Network{Address: address, Gateway: gateway, DNS: c.dns()}
	case "aether.dns":
		dns := make([]netip.Addr, 0, len(fields))
		for _, f := range fields {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return err
			}
			dns = append(dns, a)
		}
		if c.Network == nil {
			c.Network = &Network{}
		}
		c.Network.DNS = dns
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
	case "aether.sock":
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
		c.Sockets = append(c.Sockets, Socket{Port: uint32(port), Path: p})
	default:
		return errors.New("unknown argument")
	}
	return nil
}

func (c *Config) dns() []netip.Addr {
	if c.Network == nil {
		return nil
	}
	return c.Network.DNS
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
	}
	for i, m := range c.Mounts {
		switch m.Volume {
		case VolumeShare, VolumeData:
		case VolumeHome:
			if n, err := strconv.Atoi(m.Source); err != nil || n < 0 || strconv.Itoa(n) != m.Source {
				errs = append(errs, fmt.Errorf("mount %d: home source %q must be an entry index", i, m.Source))
			}
		case VolumeCache, VolumeHelper:
			if m.Source != "." {
				errs = append(errs, fmt.Errorf("mount %d: %s source must be \".\"", i, m.Volume))
			}
		default:
			errs = append(errs, fmt.Errorf("mount %d: unknown volume %q", i, m.Volume))
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
	for i, s := range c.Sockets {
		if s.Port == 0 || s.Port == APIPort {
			errs = append(errs, fmt.Errorf("socket %d: port %d is reserved", i, s.Port))
		}
		if !cleanAbs(s.Path) || s.Path == "/" {
			errs = append(errs, fmt.Errorf("socket %d: path %q must be a clean absolute path below /", i, s.Path))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("guest: invalid configuration: %w", errors.Join(errs...))
	}
	return nil
}

func cleanAbs(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsRune(p, 0)
}

func validHostname(name string) bool {
	if len(name) > 64 {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
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
