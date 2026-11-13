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
	"text/template"

	"github.com/3xDevOps/Aether/internal/egress"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// vmSubnet holds one /30 per VM: .1 is the host end of its tap, .2 the
// guest. It sits outside Docker's default address pools. The first /30 is
// no VM's: its .1 is proxyAddr.
var vmSubnet = netip.MustParsePrefix("10.200.0.0/16")

// proxyAddr is where every VM reaches the egress proxy, held by proxyLink.
var proxyAddr, _ = netAddrs(0)

const (
	// tapPrefix starts every VM tap's name and egressTapPrefix the taps of
	// VMs with egress control, so the firewall tells them apart by a name
	// the guest cannot change.
	tapPrefix       = "aethervm"
	egressTapPrefix = tapPrefix + "e"
	maxNets         = 1 << 14
	proxyLink       = "aether-vmproxy"
	firewallTable   = "aether_vm"
	firewallFile    = "firewall.nft"
	// allowedMark is the packet mark bit the firewall sets on the VM traffic
	// it forwards, clear of the bits Tailscale and Kubernetes use.
	allowedMark = 0x01000000
)

// dockerDNS is what Docker gives a container when the host resolver is
// loopback only.
var dockerDNS = []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("8.8.4.4")}

func tapName(index int, egressOnly bool) string {
	if egressOnly {
		return fmt.Sprintf("%s%d", egressTapPrefix, index)
	}
	return fmt.Sprintf("%s%d", tapPrefix, index)
}

func netAddrs(index int) (host, guestAddr netip.Addr) {
	base := vmSubnet.Addr().As4()
	n := index * 4
	base[2], base[3] = byte(n>>8), byte(n)
	host = netip.AddrFrom4(base).Next()
	return host, host.Next()
}

// guestIndex is the network whose guest address is ip.
func guestIndex(ip netip.Addr) (int, bool) {
	if !ip.Is4() || !vmSubnet.Contains(ip) {
		return 0, false
	}
	b := ip.As4()
	n := int(b[2])<<8 | int(b[3])
	if n%4 != 2 || n < 4 {
		return 0, false
	}
	return n / 4, true
}

// chNetArg lets Cloud Hypervisor create the tap and give its host end an
// address; the tap goes away with the VM.
func chNetArg(index int, egressOnly bool) string {
	host, _ := netAddrs(index)
	return fmt.Sprintf("tap=%s,ip=%s,mask=255.255.255.252,mac=02:ae:00:00:%02x:%02x", tapName(index, egressOnly), host, byte(index>>8), byte(index))
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
	for index := 1; index < maxNets; index++ {
		if !used[index] {
			e.reserved[index] = true
			return index, nil
		}
	}
	return 0, fmt.Errorf("vm runtime: all %d VM networks in %s are in use", maxNets-1, vmSubnet)
}

func (e *engine) releaseNet(index int) {
	e.netMu.Lock()
	delete(e.reserved, index)
	e.netMu.Unlock()
}

// egressKey names the VM with egress control whose guest address is ip.
// The firewall drops a packet whose source is not its own tap's guest, so
// the address names the VM it came from.
func (e *engine) egressKey(ip netip.Addr) (string, error) {
	if index, ok := guestIndex(ip); ok {
		keys, err := e.keys()
		if err != nil {
			return "", err
		}
		for _, key := range keys {
			if rec, loadErr := e.load(key); loadErr == nil && rec.Net == index && rec.Egress {
				return key, nil
			}
		}
	}
	return "", fmt.Errorf("vm runtime: no session VM with egress control has %s: %w", ip, runtime.ErrNotFound)
}

// firewall is one nft transaction that replaces the table whole. Its drops
// hold whatever other tables accept; an accept here only passes the packet
// on to them. Its input and forward chains run just before iptables', whose
// INPUT and FORWARD take only what they marked.
var firewall = template.Must(template.New("firewall").Parse(`table inet {{.Table}}
delete table inet {{.Table}}
table inet {{.Table}} {
	chain prerouting {
		type filter hook prerouting priority raw; policy accept;
		iifname "{{.Taps}}*" meta nfproto ipv6 drop
		iifname "{{.Taps}}*" fib saddr . iif oif missing drop
		iifname "{{.Taps}}*" ip daddr { 169.254.169.254, 100.100.100.200 } drop
	}
	chain input {
		type filter hook input priority filter - 1; policy accept;
		iifname "{{.EgressTaps}}*" ct state established,related meta mark set meta mark | {{.Mark}} accept
		iifname "{{.EgressTaps}}*" ip daddr {{.Proxy}} tcp dport {{.ProxyPort}} meta mark set meta mark | {{.Mark}} accept
		iifname "{{.EgressTaps}}*" reject
	}
	chain forward {
		type filter hook forward priority filter - 1; policy accept;
		iifname "{{.EgressTaps}}*" reject with icmpx admin-prohibited
{{- if .Uplinks}}
		iifname "{{.Taps}}*" oifname { {{range $i, $u := .Uplinks}}{{if $i}}, {{end}}"{{$u}}"{{end}} } meta mark set meta mark | {{.Mark}} accept
{{- end}}
{{- if .DNS}}
		iifname "{{.Taps}}*" ip daddr { {{range $i, $a := .DNS}}{{if $i}}, {{end}}{{$a}}{{end}} } meta l4proto { tcp, udp } th dport 53 meta mark set meta mark | {{.Mark}} accept
{{- end}}
		iifname "{{.Taps}}*" reject with icmpx admin-prohibited
		oifname "{{.Taps}}*" ct state established,related meta mark set meta mark | {{.Mark}} accept
		oifname "{{.Taps}}*" drop
	}
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr {{.Subnet}} ip daddr != {{.Subnet}} masquerade
	}
}
`))

// ensureNetwork sets up the host side of every VM's network. VMs leave the
// host only through its default-route interfaces, so they miss every bridge
// of Docker's, whatever it is named, and the DNS servers guests are given
// stay reachable wherever they route, as a VPN's resolver does. It runs at
// every start: the firewall is replaced in one transaction, so a changed
// default route or resolver applies to the next VM that boots.
func (e *engine) ensureNetwork(ctx context.Context) error {
	e.netMu.Lock()
	defer e.netMu.Unlock()
	if err := e.run(ctx, "sysctl", "-q", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	if e.run(ctx, "ip", "link", "show", "dev", proxyLink) != nil {
		if err := e.run(ctx, "ip", "link", "add", proxyLink, "type", "dummy"); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		{"addr", "replace", netip.PrefixFrom(proxyAddr, 32).String(), "dev", proxyLink},
		{"link", "set", proxyLink, "up"},
	} {
		if err := e.run(ctx, "ip", args...); err != nil {
			return err
		}
	}
	uplinks, err := defaultRoutes()
	if err != nil {
		return err
	}
	var rules strings.Builder
	if err = firewall.Execute(&rules, map[string]any{
		"Table": firewallTable, "Taps": tapPrefix, "EgressTaps": egressTapPrefix,
		"Proxy": proxyAddr, "ProxyPort": egress.Port, "Subnet": vmSubnet,
		"Uplinks": uplinks, "DNS": hostDNS(), "Mark": fmt.Sprintf("%#x", allowedMark),
	}); err != nil {
		return fmt.Errorf("vm runtime: write the VM firewall: %w", err)
	}
	if err = os.MkdirAll(e.root, 0o700); err != nil {
		return fmt.Errorf("vm runtime: %w", err)
	}
	path := filepath.Join(e.root, firewallFile)
	if err = writeFileAtomic(path, []byte(rules.String())); err != nil {
		return err
	}
	if err = e.run(ctx, "nft", "-f", path); err != nil {
		return err
	}
	// A DROP policy, as Docker sets on FORWARD and ufw on INPUT, would drop
	// what the table lets through; without the table it drops VM traffic.
	for _, chain := range []string{"INPUT", "FORWARD"} {
		rule := []string{chain, "-m", "mark", "--mark", fmt.Sprintf("%#x/%#x", allowedMark, allowedMark), "-j", "ACCEPT"}
		if e.run(ctx, "iptables", append([]string{"-w", "-t", "filter", "-C"}, rule...)...) == nil {
			continue
		}
		if err = e.run(ctx, "iptables", append([]string{"-w", "-t", "filter", "-I"}, rule...)...); err != nil {
			return err
		}
	}
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
