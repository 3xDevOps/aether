package vm

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestNetworkAddressing(t *testing.T) {
	host, guestAddr := netAddrs(65)
	if host.String() != "10.200.1.5" || guestAddr.String() != "10.200.1.6" {
		t.Errorf("addresses of 65 = %s, %s", host, guestAddr)
	}
	if got := chNetArg(65); got != "tap=aethervm65,ip=10.200.1.5,mask=255.255.255.252,mac=02:ae:00:00:00:41" {
		t.Errorf("net arg = %s", got)
	}
	last, _ := netAddrs(maxNets - 1)
	if !vmSubnet.Contains(last) {
		t.Errorf("last network %s leaves %s", last, vmSubnet)
	}
	n := guestNetwork(0, dockerDNS)
	if n.Address != netip.MustParsePrefix("10.200.0.2/30") || n.Gateway.String() != "10.200.0.1" {
		t.Errorf("guest network = %+v", n)
	}
	servers := nameservers(strings.NewReader("# x\nnameserver 127.0.0.53\nnameserver 10.0.0.2\nnameserver fe80::1\nsearch ec2.internal\n"))
	if !slices.Equal(servers, []netip.Addr{netip.MustParseAddr("10.0.0.2")}) {
		t.Errorf("nameservers = %v", servers)
	}
}

func TestAllocateNetSkipsRecordedAndReserved(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	first, err := h.engine.allocateNet()
	if err != nil || first != 1 {
		t.Fatalf("allocate = %d, %v", first, err)
	}
	second, err := h.engine.allocateNet()
	if err != nil || second != 2 {
		t.Fatalf("allocate while 1 is reserved = %d, %v", second, err)
	}
	h.engine.releaseNet(first)
	if again, _ := h.engine.allocateNet(); again != 1 {
		t.Errorf("allocate after release = %d", again)
	}
}

func TestEnsureNATIsolatesVMs(t *testing.T) {
	h := newHarness(t)
	e := h.engine
	e.natReady = false
	var calls []string
	e.run = func(_ context.Context, name string, args ...string) error {
		call := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, call)
		switch {
		case strings.Contains(call, "-F AETHER-VM"), strings.Contains(call, "-C FORWARD"):
			return errors.New("no such chain or rule")
		}
		return nil
	}
	if err := e.ensureNAT(t.Context()); err != nil {
		t.Fatal(err)
	}
	uplinks, err := defaultRoutes()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sysctl -q -w net.ipv4.ip_forward=1", "iptables -t filter -F AETHER-VM", "iptables -t filter -N AETHER-VM"}
	for _, uplink := range uplinks {
		want = append(want, "iptables -t filter -A AETHER-VM -i aethervm+ -o "+uplink+" -j ACCEPT")
	}
	want = append(want,
		"iptables -t filter -A AETHER-VM -i aethervm+ -j DROP",
		"iptables -t filter -A AETHER-VM -o aethervm+ -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"iptables -t filter -A AETHER-VM -o aethervm+ -j DROP",
		"iptables -t filter -C FORWARD -j AETHER-VM",
		"iptables -t filter -I FORWARD -j AETHER-VM",
		"iptables -t nat -C POSTROUTING -s 10.200.0.0/16 ! -d 10.200.0.0/16 -j MASQUERADE",
	)
	if !slices.Equal(calls, want) {
		t.Errorf("calls:\n%s", strings.Join(calls, "\n"))
	}
	calls = nil
	if err := e.ensureNAT(t.Context()); err != nil || calls != nil {
		t.Errorf("second ensureNAT ran %v, %v", calls, err)
	}
}

func TestRouteUplinksAreTheDefaultRoutes(t *testing.T) {
	table := `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
ens5	00000000	01001FAC	0003	0	0	100	00000000	0	0	0
ens5	00001FAC	00000000	0001	0	0	100	00F0FFFF	0	0	0
docker0	000011AC	00000000	0001	0	0	0	0000FFFF	0	0	0
wg0	00000000	00000000	0001	0	0	200	00000000	0	0	0
`
	if got := routeUplinks(strings.NewReader(table)); !slices.Equal(got, []string{"ens5", "wg0"}) {
		t.Errorf("uplinks = %v", got)
	}
}
