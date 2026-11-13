package vm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
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
			return 0, err
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

// ensureNAT runs once per process; a failure is retried at the next start.
func (e *engine) ensureNAT(ctx context.Context) error {
	e.netMu.Lock()
	defer e.netMu.Unlock()
	if e.natReady {
		return nil
	}
	if err := e.run(ctx, "sysctl", "-q", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	subnet, taps := vmSubnet.String(), tapPrefix+"+"
	for _, rule := range [][]string{
		{"-t", "nat", "POSTROUTING", "-s", subnet, "!", "-d", subnet, "-j", "MASQUERADE"},
		{"-t", "filter", "FORWARD", "-i", taps, "-j", "ACCEPT"},
		{"-t", "filter", "FORWARD", "-o", taps, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
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
