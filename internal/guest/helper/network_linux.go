package helper

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// configureNetwork brings up loopback and the VM's one interface, and
// writes the host files Docker writes into a container.
func configureNetwork(cfg guest.Config) error {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("find lo: %w", err)
	}
	if err = netlink.LinkSetUp(lo); err != nil {
		return fmt.Errorf("bring up lo: %w", err)
	}
	if cfg.Hostname != "" {
		if err = unix.Sethostname([]byte(cfg.Hostname)); err != nil {
			return fmt.Errorf("set the hostname: %w", err)
		}
		if err = replaceFile("/etc/hostname", cfg.Hostname+"\n"); err != nil {
			return err
		}
	}
	hosts := "127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n"
	n := cfg.Network
	if n != nil && cfg.Hostname != "" {
		hosts += n.Address.Addr().String() + "\t" + cfg.Hostname + "\n"
	}
	if err = replaceFile("/etc/hosts", hosts); err != nil {
		return err
	}
	if n == nil {
		return nil
	}
	link, err := ethernet()
	if err != nil {
		return err
	}
	address := &net.IPNet{IP: n.Address.Addr().AsSlice(), Mask: net.CIDRMask(n.Address.Bits(), 32)}
	if err = netlink.AddrAdd(link, &netlink.Addr{IPNet: address}); err != nil {
		return fmt.Errorf("address %s: %w", link.Attrs().Name, err)
	}
	if err = netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring up %s: %w", link.Attrs().Name, err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: n.Gateway.AsSlice()}
	if err = netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("default route via %s: %w", n.Gateway, err)
	}
	if len(n.DNS) == 0 {
		return nil
	}
	var resolv strings.Builder
	for _, a := range n.DNS {
		resolv.WriteString("nameserver " + a.String() + "\n")
	}
	return replaceFile("/etc/resolv.conf", resolv.String())
}

// ethernet is the VM's one network device.
func ethernet() (netlink.Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list network devices: %w", err)
	}
	for _, l := range links {
		if l.Type() == "device" && l.Attrs().Flags&net.FlagLoopback == 0 {
			return l, nil
		}
	}
	return nil, errors.New("the VM has no network device")
}

// replaceFile writes a fresh file in place of whatever the image has,
// including a symlink into a directory that does not exist in the VM.
func replaceFile(name, content string) error {
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(name, []byte(content), 0o644)
}
