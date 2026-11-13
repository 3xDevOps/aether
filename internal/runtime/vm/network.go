package vm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/guest"
)

// vmSubnet holds one /30 per VM: .1 is the host end of its tap, .2 the
// guest. It sits outside Docker's default address pools.
var vmSubnet = netip.MustParsePrefix("10.200.0.0/16")

const (
	tapPrefix = "aethervm"
	maxNets   = 1 << 14
)

// dockerDNS is what Docker gives a container when the host resolver is
// loopback only.
var dockerDNS = []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("8.8.4.4")}

func tapName(index int) string { return fmt.Sprintf("%s%d", tapPrefix, index) }

func netAddrs(index int) (host, guestAddr netip.Addr) {
	base := vmSubnet.Addr().As4()
	n := index * 4
	base[2], base[3] = byte(n>>8), byte(n)
	host = netip.AddrFrom4(base).Next()
	return host, host.Next()
}

// chNetArg lets Cloud Hypervisor create the tap and give its host end an
// address; the tap goes away with the VM.
func chNetArg(index int) string {
	host, _ := netAddrs(index)
	return fmt.Sprintf("tap=%s,ip=%s,mask=255.255.255.252,mac=02:ae:00:00:%02x:%02x", tapName(index), host, byte(index>>8), byte(index))
}

func guestNetwork(index int, dns []netip.Addr) *guest.Network {
	host, guestAddr := netAddrs(index)
	return &guest.Network{Address: netip.PrefixFrom(guestAddr, 30), Gateway: host, DNS: dns}
}

// allocateNet returns the lowest index no recorded or reserved VM holds.
// The caller releases the reservation once the record is stored.
func (e *engine) allocateNet() (int, error) {
	e.netMu.Lock()
	defer e.netMu.Unlock()
	used := make(map[int]bool, len(e.reserved))
	for index := range e.reserved {
		used[index] = true
	}
	keys, err := e.keys()
	if err != nil {
		return 0, err
	}
	for _, key := range keys {
		rec, err := e.load(key)
		if err != nil {
			slog.Warn("vm runtime: skip an unreadable VM record when allocating a network", "vm", key, "error", err)
			continue
		}
		used[rec.Net] = true
	}
	for index := range maxNets {
		if !used[index] {
			e.reserved[index] = true
			return index, nil
		}
	}
	return 0, fmt.Errorf("vm runtime: all %d VM networks in %s are in use", maxNets, vmSubnet)
}

func (e *engine) releaseNet(index int) {
	e.netMu.Lock()
	delete(e.reserved, index)
	e.netMu.Unlock()
}

// vmChain holds the VMs' forwarding rules, rebuilt whole once per process
// from firewallFile beside the VMs.
const (
	vmChain      = "AETHER-VM"
	firewallFile = "firewall.rules"
)

// ensureNAT lets VMs out only through the host's default-route interfaces,
// so they reach the internet but not each other or any bridge of Docker's,
// whatever it is named, and no connection starts from outside into a VM.
// The DNS servers guests are given stay reachable on port 53 wherever they
// route, as a VPN's resolver does. It runs once per process; a failure is
// retried at the next start.
func (e *engine) ensureNAT(ctx context.Context) error {
	e.netMu.Lock()
	defer e.netMu.Unlock()
	if e.natReady {
		return nil
	}
	if err := e.run(ctx, "sysctl", "-q", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	taps := tapPrefix + "+"
	uplinks, err := defaultRoutes()
	if err != nil {
		return err
	}
	var rules [][]string
	for _, uplink := range uplinks {
		rules = append(rules, []string{"-i", taps, "-o", uplink, "-j", "ACCEPT"})
	}
	for _, dns := range hostDNS() {
		for _, proto := range []string{"udp", "tcp"} {
			rules = append(rules, []string{"-i", taps, "-d", dns.String(), "-p", proto, "--dport", "53", "-j", "ACCEPT"})
		}
	}
	rules = append(rules,
		[]string{"-i", taps, "-j", "DROP"},
		[]string{"-o", taps, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		[]string{"-o", taps, "-j", "DROP"},
	)
	// iptables-restore swaps the chain's rules in one commit, so VMs that
	// already run are never left without them.
	restore := "*filter\n:" + vmChain + " - [0:0]\n"
	for _, rule := range rules {
		restore += "-A " + vmChain + " " + strings.Join(rule, " ") + "\n"
	}
	restore += "COMMIT\n"
	if err := os.MkdirAll(e.root, 0o700); err != nil {
		return fmt.Errorf("vm runtime: %w", err)
	}
	path := filepath.Join(e.root, firewallFile)
	if err := writeFileAtomic(path, []byte(restore)); err != nil {
		return err
	}
	if err := e.run(ctx, "iptables-restore", "--noflush", path); err != nil {
		return err
	}
	subnet := vmSubnet.String()
	for _, rule := range [][]string{
		{"-t", "filter", "FORWARD", "-j", vmChain},
		{"-t", "nat", "POSTROUTING", "-s", subnet, "!", "-d", subnet, "-j", "MASQUERADE"},
	} {
		table, chain, match := rule[:2], rule[2], rule[3:]
		check := append(append(append([]string{}, table...), "-C", chain), match...)
		if e.run(ctx, "iptables", check...) == nil {
			continue
		}
		insert := append(append(append([]string{}, table...), "-I", chain), match...)
		if err := e.run(ctx, "iptables", insert...); err != nil {
			return err
		}
	}
	e.natReady = true
	return nil
}

// defaultRoutes reads the interfaces of the host's IPv4 default routes.
func defaultRoutes() ([]string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return nil, fmt.Errorf("vm runtime: read the host's routes: %w", err)
	}
	defer func() { _ = f.Close() }()
	return routeUplinks(f), nil
}

func routeUplinks(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 8 && f[1] == "00000000" && f[7] == "00000000" && !slices.Contains(out, f[0]) {
			out = append(out, f[0])
		}
	}
	return out
}

// hostDNS prefers systemd-resolved's upstream servers, since its stub at
// 127.0.0.53 is unreachable from a guest.
func hostDNS() []netip.Addr {
	for _, path := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			break
		}
		servers := nameservers(f)
		_ = f.Close()
		if len(servers) > 0 {
			return servers
		}
	}
	return dockerDNS
}

func nameservers(r io.Reader) []netip.Addr {
	var out []netip.Addr
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		addr, err := netip.ParseAddr(fields[1])
		if err == nil && addr.Is4() && !addr.IsLoopback() {
			out = append(out, addr)
		}
	}
	return out
}
