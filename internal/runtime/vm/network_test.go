package vm

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/vishvananda/netlink"
)

func TestNetworkAddressing(t *testing.T) {
	host, guestAddr := netAddrs(65)
	if host.String() != "10.200.1.5" || guestAddr.String() != "10.200.1.6" {
		t.Errorf("addresses of 65 = %s, %s", host, guestAddr)
	}
	if got := chNetArg(65, false); got != "tap=aethervm65,ip=10.200.1.5,mask=255.255.255.252,mac=02:ae:00:00:00:41" {
		t.Errorf("net arg = %s", got)
	}
	if got := chNetArg(65, true); !strings.HasPrefix(got, "tap=aethervme65,") {
		t.Errorf("egress net arg = %s", got)
	}
	last, _ := netAddrs(maxNets - 1)
	if !vmSubnet.Contains(last) {
		t.Errorf("last network %s leaves %s", last, vmSubnet)
	}
	if proxyAddr.String() != "10.200.0.1" {
		t.Errorf("proxy address = %s", proxyAddr)
	}
	for addr, want := range map[string]int{"10.200.1.6": 65, "10.200.0.6": 1, "10.200.255.254": maxNets - 1} {
		if got, ok := guestIndex(netip.MustParseAddr(addr)); !ok || got != want {
			t.Errorf("guest index of %s = %d, %v", addr, got, ok)
		}
	}
	for _, addr := range []string{"10.200.0.2", "10.200.1.5", "10.200.1.7", "10.200.1.4", "10.201.0.6", "::ffff:10.200.1.6"} {
		if index, ok := guestIndex(netip.MustParseAddr(addr)); ok {
			t.Errorf("%s is no guest's but read as network %d", addr, index)
		}
	}
	n := guestNetwork(1)
	if n.Address != netip.MustParsePrefix("10.200.0.6/30") || n.Gateway.String() != "10.200.0.5" {
		t.Errorf("guest network = %+v", n)
	}
	servers, search := resolvConf(strings.NewReader("# x\nnameserver 127.0.0.53\nnameserver 10.0.0.2\nnameserver fe80::1%eth0\nsearch ec2.internal\n"))
	if want := []netip.Addr{netip.MustParseAddr("127.0.0.53"), netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("fe80::1%eth0")}; !slices.Equal(servers, want) {
		t.Errorf("nameservers = %v", servers)
	}
	if !slices.Equal(search, []string{"ec2.internal"}) {
		t.Errorf("search = %v", search)
	}
	for conf, want := range map[string][]string{
		"search corp.example tailnet.ts.net.\n":             {"corp.example", "tailnet.ts.net"},
		"search .\n":                                        nil,
		"search a.example\ndomain b.example\n":              {"b.example"},
		"search bad_label.example ok.example\n":             {"ok.example"},
		"search a b c d e f g\n":                            {"a", "b", "c", "d", "e", "f"},
		"search corp.example # managed by NetworkManager\n": {"corp.example"},
		"search " + strings.Repeat("x", 63) + ".example " + strings.Repeat("y", 63) + ".example " + strings.Repeat("z", 63) + ".example " + strings.Repeat("w", 63) + ".example\n": {strings.Repeat("x", 63) + ".example", strings.Repeat("y", 63) + ".example", strings.Repeat("z", 63) + ".example"},
	} {
		if _, got := resolvConf(strings.NewReader(conf)); !slices.Equal(got, want) {
			t.Errorf("search of %q = %v, want %v", conf, got, want)
		}
	}
}

func TestAllocateNetSkipsRecordedAndReserved(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	first, err := h.engine.allocateNet()
	if err != nil || first != 2 {
		t.Fatalf("allocate = %d, %v", first, err)
	}
	second, err := h.engine.allocateNet()
	if err != nil || second != 3 {
		t.Fatalf("allocate while 2 is reserved = %d, %v", second, err)
	}
	h.engine.releaseNet(first)
	if again, _ := h.engine.allocateNet(); again != 2 {
		t.Errorf("allocate after release = %d", again)
	}
}

func TestEgressVMsAreNamedByTheirAddress(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-open", sessionSpec("run-open"))
	if args := strings.Join(h.units.last().launch.Args, " "); !strings.Contains(args, "aether.dns=10.200.0.1 ") {
		t.Errorf("an open VM resolves past the server's DNS relay:\n%s", args)
	}
	spec := sessionSpec("run-egress")
	spec.Egress = true
	h.createStarted("run-egress", spec)
	args := strings.Join(h.units.last().launch.Args, " ")
	if !strings.Contains(args, "tap=aethervme2,ip=10.200.0.9,") || strings.Contains(args, "aether.dns=") {
		t.Errorf("an egress VM's launch names another tap or gets the host's resolvers:\n%s", args)
	}
	open, _ := netAddrs(1)
	_, egressGuest := netAddrs(2)
	if key, err := h.engine.egressKey(egressGuest); err != nil || key != "run-egress" {
		t.Errorf("egress key of %s = %q, %v", egressGuest, key, err)
	}
	for _, addr := range []netip.Addr{open.Next(), open, proxyAddr} {
		if key, err := h.engine.egressKey(addr); !errors.Is(err, runtime.ErrNotFound) {
			t.Errorf("egress key of %s = %q, %v", addr, key, err)
		}
	}
}

func TestEnsureNetworkAddsWhatIsMissingOnce(t *testing.T) {
	h := newHarness(t)
	e := h.engine
	linked, inserted := false, map[string]bool{}
	var calls []string
	e.run = func(_ context.Context, name string, args ...string) error {
		call := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, call)
		switch {
		case call == "ip link show dev aether-vmproxy" && !linked:
			return errors.New("Device does not exist")
		case call == "ip link add aether-vmproxy type dummy":
			linked = true
		case strings.Contains(call, " -C ") && !inserted[strings.Replace(call, " -C ", " -I ", 1)]:
			return errors.New("Bad rule")
		case strings.HasPrefix(call, "iptables -w -t filter -I "):
			inserted[call] = true
		}
		return nil
	}
	for range 2 {
		if err := e.ensureNetwork(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	count := func(prefix string) (n int) {
		for _, c := range calls {
			if strings.HasPrefix(c, prefix) {
				n++
			}
		}
		return n
	}
	if count("ip link add aether-vmproxy") != 1 || count("iptables -w -t filter -I FORWARD -m mark") != 1 || count("iptables -w -t filter -I INPUT -m mark") != 1 || count("nft -f "+filepath.Join(e.root, firewallFile)) != 2 {
		t.Errorf("calls:\n%s", strings.Join(calls, "\n"))
	}
	if count("ip addr replace 10.200.0.1/32 dev aether-vmproxy") != 2 {
		t.Errorf("the proxy address is not kept on its link:\n%s", strings.Join(calls, "\n"))
	}
	rules, err := os.ReadFile(filepath.Join(e.root, firewallFile))
	if err != nil {
		t.Fatal(err)
	}
	udp, tcp := e.dns.ports()
	relayed := "10.200.0.1"
	for _, dns := range hostDNS() {
		relayed += ", " + dns.String()
	}
	for _, want := range []string{
		fmt.Sprintf(`iifname "aethervm*" ip daddr { %s } udp dport 53 dnat ip to 10.200.0.1:%d`, relayed, udp),
		fmt.Sprintf(`iifname "aethervm*" ip daddr { %s } tcp dport 53 dnat ip to 10.200.0.1:%d`, relayed, tcp),
		fmt.Sprintf(`iifname "aethervm*" ip daddr 10.200.0.1 udp dport %d meta mark set meta mark | 0x1000000 accept`, udp),
		fmt.Sprintf(`iifname "aethervm*" ip daddr 10.200.0.1 tcp dport %d meta mark set meta mark | 0x1000000 accept`, tcp),
		fmt.Sprintf("ip daddr 10.200.0.1 udp dport %d drop", udp),
		"type nat hook prerouting priority dstnat - 1;",
		`iifname "aethervm*" meta nfproto ipv6 drop`,
		`iifname "aethervm*" fib saddr . iif oif missing drop`,
		`meta l4proto { tcp, udp } th dport 53 accept`,
		`iifname "aethervm*" ip daddr { 169.254.0.0/16, 100.100.100.200, 168.63.129.16, 100.100.100.100 } drop`,
		`iifname "aethervme*" ct state established,related meta mark set meta mark | 0x1000000 accept`,
		`iifname "aethervme*" ip daddr 10.200.0.1 tcp dport 3128 meta mark set meta mark | 0x1000000 accept`,
		"iifname \"aethervme*\" reject with icmpx admin-prohibited\n\t\tiifname \"aethervm*\" tcp flags syn tcp option maxseg size set rt mtu\n",
		"iifname \"aethervm*\" reject with icmpx admin-prohibited\n\t\toifname \"aethervm*\" ct state established,related",
		`oifname "aethervm*" ct state established,related meta mark set meta mark | 0x1000000 accept`,
		`oifname "aethervm*" drop`,
	} {
		if !strings.Contains(string(rules), want) {
			t.Errorf("the firewall lacks %q:\n%s", want, rules)
		}
	}
	// The DNS exception must come before the link-local drop; after it, a
	// resolver inside the blocked ranges would be cut off.
	if accept, drop := strings.Index(string(rules), "th dport 53 accept"), strings.Index(string(rules), "169.254.0.0/16, 100.100.100.200, 168.63.129.16, 100.100.100.100 } drop"); accept < 0 || drop < 0 || accept > drop {
		t.Errorf("the DNS exception must precede the link-local drop (accept=%d, drop=%d):\n%s", accept, drop, rules)
	}
}

func TestUplinksAreTheHostsOwnLinks(t *testing.T) {
	link := func(name string, index, master int) netlink.LinkAttrs {
		return netlink.LinkAttrs{Name: name, Index: index, MasterIndex: master}
	}
	encap := func(l netlink.Link, encapType string) netlink.Link {
		l.Attrs().EncapType = encapType
		return l
	}
	vlan := func(name string, index, parent, master int) *netlink.Vlan {
		l := &netlink.Vlan{LinkAttrs: link(name, index, master)}
		l.ParentIndex = parent
		return l
	}
	links := []netlink.Link{
		&netlink.Device{LinkAttrs: link("lo", 1, 0)},
		&netlink.Device{LinkAttrs: link("ens5", 2, 0)},
		&netlink.Bridge{LinkAttrs: link("br-ens3", 3, 0)},
		&netlink.Device{LinkAttrs: link("ens3", 4, 3)},
		&netlink.Bridge{LinkAttrs: link("docker0", 5, 0)},
		&netlink.Veth{LinkAttrs: link("veth1", 6, 5)},
		&netlink.Bridge{LinkAttrs: link("virbr0", 7, 0)},
		encap(&netlink.Tuntap{LinkAttrs: link("vnet0", 8, 7)}, "ether"),
		&netlink.Bridge{LinkAttrs: link("lxdfan0", 9, 0)},
		&netlink.Dummy{LinkAttrs: link("lxdfan0-mtu", 10, 9)},
		&netlink.Vxlan{LinkAttrs: link("lxdfan0-fan", 11, 9)},
		&netlink.Veth{LinkAttrs: link("cali1a2b3c", 12, 0)},
		&netlink.GenericLink{LinkAttrs: link("ovn-k8s-mp0", 13, 0), LinkType: "openvswitch"},
		&netlink.Netkit{LinkAttrs: link("lxc42", 14, 0)},
		encap(&netlink.Tuntap{LinkAttrs: link("fc-tap0", 15, 0)}, "ether"),
		encap(&netlink.Tuntap{LinkAttrs: link("tailscale0", 16, 0)}, "none"),
		&netlink.Wireguard{LinkAttrs: link("wg0", 17, 0)},
		&netlink.Bond{LinkAttrs: link("bond0", 18, 0)},
		&netlink.Device{LinkAttrs: link("eth1", 19, 18)},
		vlan("bond0.10", 20, 18, 0),
		&netlink.GenericLink{LinkAttrs: link("team0", 21, 0), LinkType: "team"},
		&netlink.Device{LinkAttrs: link("vboxnet0", 22, 0)},
		&netlink.Dummy{LinkAttrs: link("aether-vmproxy", 23, 0)},
		encap(&netlink.Tuntap{LinkAttrs: link("aethervm1", 24, 0)}, "ether"),
		&netlink.Veth{LinkAttrs: link("eth0", 25, 0)},
		&netlink.Bridge{LinkAttrs: link("vmbr1", 26, 0)},
		encap(&netlink.Tuntap{LinkAttrs: link("tap100i0", 27, 26)}, "ether"),
		vlan("vmbr1.10", 28, 26, 0),
		&netlink.Bridge{LinkAttrs: link("br-lan", 29, 0)},
		vlan("ens5.20", 30, 2, 29),
		&netlink.GenericLink{LinkAttrs: link("tun1", 31, 0), LinkType: "ovpn-dco"},
		&netlink.Xfrmi{LinkAttrs: link("ipsec0", 32, 0)},
		encap(&netlink.GenericLink{LinkAttrs: link("ppp0", 33, 0), LinkType: "ppp"}, "ppp"),
		&netlink.Bond{LinkAttrs: link("bond1", 34, 0)},
		&netlink.Veth{LinkAttrs: link("veth2", 35, 34)},
		&netlink.Device{LinkAttrs: link("eth2", 36, 21)},
	}
	physical := map[int]bool{2: true, 4: true, 19: true, 36: true}
	want := []string{"ens5", "br-ens3", "ens3", "tailscale0", "wg0", "bond0", "eth1", "bond0.10", "team0", "br-lan", "ens5.20", "tun1", "ipsec0", "ppp0", "eth2"}
	if got := uplinks(links, physical, nil); !slices.Equal(got, want) {
		t.Errorf("uplinks = %v, want %v", got, want)
	}
	// A server in a system container leaves by its veth.
	want = slices.Insert(want, 9, "eth0")
	if got := uplinks(links, physical, []int{25, 2}); !slices.Equal(got, want) {
		t.Errorf("uplinks with the internet through eth0 = %v, want %v", got, want)
	}
}
