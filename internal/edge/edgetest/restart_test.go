package edgetest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// deadAddr is a loopback address nothing listens on.
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestEdgeRestartAndDirectFallback(t *testing.T) {
	h := newHarness(t)
	a := h.newServer()
	al := h.login(alice)[0]
	h.claimServer(al, a)
	relayed := h.mustDial(al, a)
	both := cli.Config{ServerID: a.id, EdgeURL: h.origin, Addr: a.addr}
	deadDirect := cli.Config{ServerID: a.id, EdgeURL: h.origin, Addr: deadAddr(t)}

	h.stopEdge()
	closedWithin(t, "relayed connection when the edge stops", relayed)

	// The server keeps serving its direct address, and a link that has
	// one uses it while the edge is down.
	sc, err := h.dial(al, both)
	if err != nil {
		t.Fatalf("direct address while the edge is down: %v", err)
	}
	_ = sc.Close()
	_, err = h.dial(al, deadDirect)
	if err == nil || !strings.Contains(err.Error(), "direct: ") || !strings.Contains(err.Error(), "edge: ") {
		t.Fatalf("neither path reachable: %v, want both causes", err)
	}

	// The server enrolls again once the edge is back, still claimed.
	h.startEdge()
	eventually(t, "server back on the restarted edge", func() error {
		if !h.relay().Online(a.id) {
			return errors.New("not online")
		}
		return nil
	})
	h.mustDial(al, a)
	sc, err = h.dial(al, deadDirect)
	if err != nil {
		t.Fatalf("edge after the direct address failed: %v", err)
	}
	_ = sc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	servers, err := al.edge.Servers(ctx)
	if err != nil || len(servers) != 1 || servers[0].ID != a.id || servers[0].Role != "admin" {
		t.Fatalf("servers after the restart = %+v %v", servers, err)
	}
}

// TestServerRemovedWhileOffline removes a server on the edge's Servers
// page while it is disconnected. When it comes back the edge knows no
// owner, and a new claim code claims it again.
func TestServerRemovedWhileOffline(t *testing.T) {
	h := newHarness(t)
	a := h.newServer()
	al := h.login(alice)[0]
	h.claimServer(al, a)
	b, err := h.signIn(alice)
	if err != nil {
		t.Fatal(err)
	}

	h.proxy.cutServers()
	eventually(t, "server off the edge", func() error {
		if h.relay().Online(a.id) {
			return errors.New("still online")
		}
		return nil
	})
	resp, page, err := b.post("/servers/remove", url.Values{"server": {a.id}})
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove on the Servers page: %v %v\n%s", err, resp, page)
	}
	from := h.proxy.mark()
	h.proxy.restoreServers()
	h.proxy.await(t, "the server back, unclaimed", from, func(e logEntry) bool {
		r, ok := e.msg.(edgeproto.Ready)
		return ok && r.ServerID == a.id && r.State == edgeproto.StateUnclaimed
	})

	_, err = h.dial(al, h.link(a))
	wantRefusal(t, "connect to a removed server", err, edgeproto.RefusalUnknownServer)
	h.claimServer(al, a)
	h.mustDial(al, a)
}
