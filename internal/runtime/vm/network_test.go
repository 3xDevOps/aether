package vm

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
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
	n := guestNetwork(1, dockerDNS)
	if n.Address != netip.MustParsePrefix("10.200.0.6/30") || n.Gateway.String() != "10.200.0.5" {
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
		case strings.Contains(call, " -C FORWARD ") && !inserted[strings.Replace(call, " -C ", " -I ", 1)]:
			return errors.New("Bad rule")
		case strings.Contains(call, " -I FORWARD "):
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
	if count("ip link add aether-vmproxy") != 1 || count("iptables -t filter -I FORWARD") != 2 || count("nft -f "+filepath.Join(e.root, firewallFile)) != 2 {
		t.Errorf("calls:\n%s", strings.Join(calls, "\n"))
	}
	if count("ip addr replace 10.200.0.1/32 dev aether-vmproxy") != 2 {
		t.Errorf("the proxy address is not kept on its link:\n%s", strings.Join(calls, "\n"))
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
