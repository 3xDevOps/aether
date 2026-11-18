package vm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/3xDevOps/Aether/internal/egress"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/vishvananda/netlink"
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

func guestNetwork(index int) *guest.Network {
	host, guestAddr := netAddrs(index)
	return &guest.Network{Address: netip.PrefixFrom(guestAddr, 30), Gateway: host}
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
//
// Forward lets an open VM out only by the host's own links, which uplinks
// lists. It clamps the MSS of a VM's SYNs to the route's MTU, as the host's
// own TCP sizes its segments: the guest sees only its tap's 1500 bytes, so
// through a smaller uplink such as tailscale0 or wg0 a server's full-size
// replies would reach it only where path MTU discovery works end to end.
//
// Prerouting drops the whole link-local range 169.254.0.0/16 from open taps,
// not just the two metadata IPs: it carries cloud metadata, credential and
// resolver endpoints (IMDS, the AWS VPC DNS at 169.254.169.253, ECS task
// credentials) that a VM has no business reaching, and nothing Aether needs
// (the gateway and proxy are in 10.200.0.0/16). Alibaba's 100.100.100.200 and
// Azure's WireServer 168.63.129.16, which serve the same role outside that
// range, are dropped too, and so is 100.100.100.100, where tailscaled serves
// the host Taildrive and its web client. The rest of the shared-address range
// 100.64.0.0/10 is not: an open VM reaches tailnet peers, as a Docker
// container on the host does. Guests resolve through the DNS relay at the
// proxy address. Port 53 to the host's upstream resolvers goes
// to the relay too, even at a link-local address: a VM booted before the
// relay still names them, and a guest often cannot reach them.
var firewall = template.Must(template.New("firewall").Parse(`table inet {{.Table}}
delete table inet {{.Table}}
table inet {{.Table}} {
	chain prerouting {
		type filter hook prerouting priority raw; policy accept;
		iifname "{{.Taps}}*" meta nfproto ipv6 drop
		iifname "{{.Taps}}*" fib saddr . iif oif missing drop
{{- if .DNS}}
		iifname "{{.Taps}}*" ip daddr { {{range $i, $a := .DNS}}{{if $i}}, {{end}}{{$a}}{{end}} } meta l4proto { tcp, udp } th dport 53 accept
{{- end}}
		iifname "{{.Taps}}*" ip daddr { 169.254.0.0/16, 100.100.100.200, 168.63.129.16, 100.100.100.100 } drop
	}
	chain dns {
		type nat hook prerouting priority dstnat - 1; policy accept;
		iifname "{{.Taps}}*" ip daddr { {{.Proxy}}{{range .DNS}}, {{.}}{{end}} } udp dport 53 dnat ip to {{.Proxy}}:{{.DNSUDP}}
		iifname "{{.Taps}}*" ip daddr { {{.Proxy}}{{range .DNS}}, {{.}}{{end}} } tcp dport 53 dnat ip to {{.Proxy}}:{{.DNSTCP}}
	}
	chain input {
		type filter hook input priority filter - 1; policy accept;
		iifname "{{.EgressTaps}}*" ct state established,related meta mark set meta mark | {{.Mark}} accept
		iifname "{{.EgressTaps}}*" ip daddr {{.Proxy}} tcp dport {{.ProxyPort}} meta mark set meta mark | {{.Mark}} accept
		iifname "{{.EgressTaps}}*" reject
		iifname "{{.Taps}}*" ip daddr {{.Proxy}} udp dport {{.DNSUDP}} meta mark set meta mark | {{.Mark}} accept
		iifname "{{.Taps}}*" ip daddr {{.Proxy}} tcp dport {{.DNSTCP}} meta mark set meta mark | {{.Mark}} accept
		ip daddr {{.Proxy}} udp dport {{.DNSUDP}} drop
		ip daddr {{.Proxy}} tcp dport {{.DNSTCP}} drop
	}
	chain forward {
		type filter hook forward priority filter - 1; policy accept;
		iifname "{{.EgressTaps}}*" reject with icmpx admin-prohibited
		iifname "{{.Taps}}*" tcp flags syn tcp option maxseg size set rt mtu
{{- if .Uplinks}}
		iifname "{{.Taps}}*" oifname { {{range $i, $u := .Uplinks}}{{if $i}}, {{end}}"{{$u}}"{{end}} } meta mark set meta mark | {{.Mark}} accept
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

// ensureNetwork sets up the host side of every VM's network. It runs at
// every start: the firewall is replaced in one transaction, so a new uplink
// or a changed resolver list applies to every VM at the next server start,
// VM boot or restore.
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
	links, err := hostUplinks()
	if err != nil {
		return err
	}
	if e.dns == nil {
		if e.dns, err = e.listenDNS(); err != nil {
			return err
		}
	}
	dnsUDP, dnsTCP := e.dns.ports()
	var rules strings.Builder
	if err = firewall.Execute(&rules, map[string]any{
		"Table": firewallTable, "Taps": tapPrefix, "EgressTaps": egressTapPrefix,
		"Proxy": proxyAddr, "ProxyPort": egress.Port, "Subnet": vmSubnet, "DNSUDP": dnsUDP, "DNSTCP": dnsTCP,
		"Uplinks": links, "DNS": hostDNS(), "Mark": fmt.Sprintf("%#x", allowedMark),
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

// hostUplinks reads the host's links and the ones its internet routes take.
func hostUplinks() ([]string, error) {
	var links []netlink.Link
	var err error
	// A link added or removed mid-read interrupts it, and the partial list
	// could leave out the NIC VMs leave by.
	for range 5 {
		if links, err = netlink.LinkList(); !errors.Is(err, netlink.ErrDumpInterrupted) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("vm runtime: read the host's interfaces: %w", err)
	}
	physical := map[int]bool{}
	for _, l := range links {
		_, statErr := os.Stat(filepath.Join("/sys/class/net", l.Attrs().Name, "device"))
		physical[l.Attrs().Index] = statErr == nil
	}
	var internet []int
	for _, probe := range []string{"1.1.1.1", "128.0.0.1"} {
		// No route to a probe means no uplink toward it, not a failure.
		routes, _ := netlink.RouteGet(net.ParseIP(probe))
		for _, r := range routes {
			internet = append(internet, r.LinkIndex)
		}
	}
	return uplinks(links, physical, internet), nil
}

// uplinks names the links an open VM's traffic may leave by: the host's LAN
// and VPN links and the links of its internet routes. A LAN link is a NIC, a
// VLAN on a LAN link, or a bond, team or bridge with one as a port. A VPN link
// is WireGuard, a layer-3 tun such as tailscale0 or tun0, OpenVPN's offload,
// an IPsec xfrm or vti link, or PPP. Veths, taps, dummies, overlays, Open
// vSwitch ports, bridges of only those and every other kind are left out:
// containers and VMs sit behind them.
func uplinks(links []netlink.Link, physical map[int]bool, internet []int) []string {
	byIndex := make(map[int]netlink.Link, len(links))
	for _, l := range links {
		byIndex[l.Attrs().Index] = l
	}
	var lan func(l netlink.Link) bool
	lan = func(l netlink.Link) bool {
		switch l.Type() {
		case "vlan":
			parent, ok := byIndex[l.Attrs().ParentIndex]
			return ok && lan(parent)
		case "bond", "team", "bridge":
			return slices.ContainsFunc(links, func(port netlink.Link) bool {
				return port.Attrs().MasterIndex == l.Attrs().Index && lan(port)
			})
		}
		return physical[l.Attrs().Index]
	}
	vpn := func(l netlink.Link) bool {
		switch l.Type() {
		case "wireguard", "ovpn", "ovpn-dco", "xfrm", "vti", "vti6":
			return true
		case "tuntap":
			return l.Attrs().EncapType == "none"
		}
		return l.Attrs().EncapType == "ppp"
	}
	var out []string
	for _, l := range links {
		if lan(l) || vpn(l) || slices.Contains(internet, l.Attrs().Index) {
			out = append(out, l.Attrs().Name)
		}
	}
	return out
}

// hostDNS are the host's upstream IPv4 servers, the ones VMs booted before
// the DNS relay name. It prefers systemd-resolved's list, since its stub at
// 127.0.0.53 is the host's own.
func hostDNS() []netip.Addr {
	for _, path := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			break
		}
		all, _ := resolvConf(f)
		servers := slices.DeleteFunc(all, func(a netip.Addr) bool { return !a.Is4() || a.IsLoopback() })
		_ = f.Close()
		if len(servers) > 0 {
			return servers
		}
	}
	return dockerDNS
}

// resolvConf reads a resolv.conf's servers and search domains. As in glibc,
// the last search or domain line wins; a domain that is not a valid name,
// such as systemd-resolved's "." for none, is left out. The domains stop at
// glibc's old limits of six and 256 bytes, which also keep a long host list
// from pushing the guest's kernel command line past its limit.
func resolvConf(r io.Reader) (servers []netip.Addr, search []string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line, _, _ = strings.Cut(line, ";")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "nameserver":
			if addr, err := netip.ParseAddr(fields[1]); err == nil {
				servers = append(servers, addr)
			}
		case "search", "domain":
			search = search[:0]
			size := 0
			for _, d := range fields[1:] {
				d = strings.TrimSuffix(d, ".")
				if !guest.ValidDomain(d) {
					continue
				}
				if size += len(d) + 1; len(search) == dnsMaxSearch || size > dnsMaxSearchBytes {
					break
				}
				search = append(search, d)
			}
		}
	}
	return servers, search
}
