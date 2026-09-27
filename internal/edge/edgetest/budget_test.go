package edgetest

import (
	"crypto/tls"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// Reaching a server's dashboard host name needs no sign-in, so anyone can
// fill its dashboard budget. With that budget full, from several
// addresses, the owner still reaches the server over SSH.
func TestDashboardConnectionsCannotStarveSSH(t *testing.T) {
	w := newWebHarness(t)
	a := w.newServer()
	al := w.login(alice)[0]
	w.claimServer(al, a)
	d := w.serveDashboard(a)

	// One address holds at most edgeproto.MaxWebConnsPerAddress of a
	// server's dashboard connections; loopback has an address to spare
	// for each share.
	handshake := func(from int) (*tls.Conn, error) {
		dialer := &net.Dialer{Timeout: waitTimeout, LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, byte(from))}}
		raw, err := dialer.Dial("tcp", w.router)
		if err != nil {
			return nil, err
		}
		c := tls.Client(raw, &tls.Config{ServerName: d.host, RootCAs: w.roots, MinVersion: tls.VersionTLS12})
		_ = c.SetDeadline(time.Now().Add(waitTimeout))
		if err := c.Handshake(); err != nil {
			_ = c.Close()
			return nil, err
		}
		return c, nil
	}
	conns := make([]*tls.Conn, edgeproto.MaxWebConnsPerServer)
	errs := make([]error, len(conns))
	var wg sync.WaitGroup
	for i := range conns {
		wg.Go(func() { conns[i], errs[i] = handshake(2 + i/edgeproto.MaxWebConnsPerAddress) })
	}
	wg.Wait()
	t.Cleanup(func() {
		for _, c := range conns {
			if c != nil {
				_ = c.Close()
			}
		}
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("dashboard connection %d of %d: %v", i+1, len(conns), err)
		}
	}
	from := 2 + len(conns)/edgeproto.MaxWebConnsPerAddress
	if c, err := handshake(from); err == nil {
		_ = c.Close()
		t.Fatalf("dashboard connection %d completed; the budget is %d", len(conns)+1, len(conns))
	}

	sc, err := w.dial(al, w.link(a))
	if err != nil {
		t.Fatalf("SSH with the dashboard budget full: %v", err)
	}
	_ = sc.Close()
}
