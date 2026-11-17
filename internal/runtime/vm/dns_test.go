package vm

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeResolver answers name's A query with answer over UDP and TCP on one
// loopback port and refuses every other query, as a resolver only the host
// can reach that serves one zone would.
func fakeResolver(t *testing.T, name string, answer netip.Addr) netip.AddrPort {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := tcp.Addr().(*net.TCPAddr).AddrPort()
	udp, err := net.ListenPacket("udp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tcp.Close(); _ = udp.Close() })
	reply := func(query []byte) []byte {
		var p dnsmessage.Parser
		h, err := p.Start(query)
		if err != nil {
			return nil
		}
		q, err := p.Question()
		if err != nil {
			return nil
		}
		if q.Name.String() != name || q.Type != dnsmessage.TypeA {
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeRefused})
			out, _ := b.Finish()
			return out
		}
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true})
		_ = b.StartQuestions()
		_ = b.Question(q)
		_ = b.StartAnswers()
		_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: answer.As4()})
		out, _ := b.Finish()
		return out
	}
	go func() {
		buf := make([]byte, 1<<16)
		for {
			n, from, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if out := reply(buf[:n]); out != nil {
				_, _ = udp.WriteTo(out, from)
			}
		}
	}()
	go func() {
		for c, err := tcp.Accept(); err == nil; c, err = tcp.Accept() {
			if query, err := readDNSMessage(c); err == nil {
				if out := reply(query); out != nil {
					_ = writeDNSMessage(c, out)
				}
			}
			_ = c.Close()
		}
	}()
	return addr
}

// refusingResolver answers every query REFUSED, as a resolver that does not
// serve the host's network would.
func refusingResolver(t *testing.T) netip.AddrPort {
	t.Helper()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })
	go func() {
		buf := make([]byte, 1<<16)
		for {
			n, from, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			h, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeRefused})
			out, _ := b.Finish()
			_, _ = udp.WriteTo(out, from)
		}
	}()
	return udp.LocalAddr().(*net.UDPAddr).AddrPort()
}

func lookupVia(network, server string, timeout time.Duration) ([]netip.Addr, error) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.LookupNetIP(ctx, "ip4", "relay.aether.test")
}

func TestDNSRelayAnswersFromTheHostsResolver(t *testing.T) {
	want := netip.MustParseAddr("192.0.2.7")
	resolver := fakeResolver(t, "relay.aether.test.", want)
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := gone.Addr().(*net.TCPAddr).AddrPort()
	_ = gone.Close()
	loopback := netip.MustParseAddr("127.0.0.1")
	upstreams := func() []netip.AddrPort { return []netip.AddrPort{dead, resolver} }

	r, err := listenDNS(loopback, netip.MustParsePrefix("127.0.0.0/8"), upstreams)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	udpPort, tcpPort := r.ports()
	refusing := refusingResolver(t)
	past, err := listenDNS(loopback, netip.MustParsePrefix("127.0.0.0/8"), func() []netip.AddrPort { return []netip.AddrPort{refusing, resolver} })
	if err != nil {
		t.Fatal(err)
	}
	defer past.close()
	pastPort, _ := past.ports()
	if got, lookupErr := lookupVia("udp", netip.AddrPortFrom(loopback, pastPort).String(), 2*dnsTimeout); lookupErr != nil || !slices.Equal(got, []netip.Addr{want}) {
		t.Errorf("lookup past a resolver that refuses = %v, %v", got, lookupErr)
	}
	for network, port := range map[string]uint16{"udp": udpPort, "tcp": tcpPort} {
		got, lookupErr := lookupVia(network, netip.AddrPortFrom(loopback, port).String(), 2*dnsTimeout)
		if lookupErr != nil || !slices.Equal(got, []netip.Addr{want}) {
			t.Errorf("lookup over %s past a dead resolver = %v, %v", network, got, lookupErr)
		}
	}

	denied, err := listenDNS(loopback, netip.MustParsePrefix("10.200.0.0/16"), upstreams)
	if err != nil {
		t.Fatal(err)
	}
	defer denied.close()
	udpPort, tcpPort = denied.ports()
	for network, port := range map[string]uint16{"udp": udpPort, "tcp": tcpPort} {
		if got, err := lookupVia(network, netip.AddrPortFrom(loopback, port).String(), time.Second); err == nil {
			t.Errorf("a relay for VMs answered a host process over %s: %v", network, got)
		}
	}
}

func TestDNSRelayLimitsEachVM(t *testing.T) {
	r := &dnsRelay{allowed: vmSubnet, perVM: map[netip.Addr]int{}}
	a, b := netip.MustParseAddr("10.200.0.6"), netip.MustParseAddr("10.200.0.10")
	for range dnsMaxPerVM {
		if !r.take(a) {
			t.Fatal("a VM was refused a query below its limit")
		}
	}
	if r.take(a) {
		t.Error("a VM took more than its share of the relay")
	}
	if !r.take(b) {
		t.Error("one VM's queries kept another VM from the relay")
	}
	r.release(a)
	if !r.take(a) {
		t.Error("a released slot was not given back")
	}
	if r.take(netip.MustParseAddr("192.168.1.5")) || r.take(netip.Addr{}) {
		t.Error("the relay took a client outside the VM range")
	}
}

func TestDNSRelayAnswersServfailWhenNoUpstreamDoes(t *testing.T) {
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := gone.Addr().(*net.TCPAddr).AddrPort()
	_ = gone.Close()
	loopback := netip.MustParseAddr("127.0.0.1")
	r, err := listenDNS(loopback, netip.MustParsePrefix("127.0.0.0/8"), func() []netip.AddrPort { return []netip.AddrPort{dead} })
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	udpPort, _ := r.ports()
	start := time.Now()
	_, err = lookupVia("udp", netip.AddrPortFrom(loopback, udpPort).String(), 4*time.Second)
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || dnsErr.IsTimeout || time.Since(start) > time.Second {
		t.Errorf("a lookup no upstream answers = %v after %s, want a prompt server failure", err, time.Since(start))
	}
}
