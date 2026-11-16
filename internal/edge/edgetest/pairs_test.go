package edgetest

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/edge/relay"
)

// An edge that starts serving a second origin pair keeps serving the
// server and the client of its first, and a server enrolled at either
// pair is reached by a client signed in at the other: every grant names
// the relay origin its server enrolled at, which the server checks,
// whichever relay origin the client came in on. The server's agent reads
// nothing of the list of relay origins the edge now sends.
func TestOriginPairs(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	relayA := h.relayURL
	from := h.proxy.mark()
	a := h.newServer(edgeproto.PolicyAccount)
	ready := h.readyTo(t, a, from)
	if ready.RelayOrigins != nil {
		t.Fatalf("ready from an edge with one pair lists relay origins %v", ready.RelayOrigins)
	}
	alA := h.login(alice)[0]
	h.claimServer(alA, a)

	h.stopEdge()
	h.pairB = true
	from = h.proxy.mark()
	h.startEdge()
	signinB, relayB := h.signinB, h.relayB
	eventually(t, "the first pair's server back on the edge", func() error {
		if !h.relay().Online(a.id) {
			return errors.New("not online")
		}
		return nil
	})
	if got := h.readyTo(t, a, from).RelayOrigins; !slices.Equal(got, []string{relayA, relayB}) {
		t.Fatalf("ready lists relay origins %v, want %s and %s", got, relayA, relayB)
	}
	h.mustDial(alA, a)

	h.signinURL, h.relayURL = signinB, relayB
	b := h.newServer(edgeproto.PolicyAccount)
	alB := h.login(alice)[0]
	if s, err := alB.edge.Session(); err != nil || s.SigninOrigin != signinB {
		t.Fatalf("session of the install signed in at %s: %+v, %v", relayB, s, err)
	}

	// Alice, signed in at the second pair, reaches the first pair's server
	// through the second pair's relay origin, and claims and reaches the
	// second pair's server.
	dialVia(t, h, alB, a, relayB, relayA)
	h.claimServer(alB, b)
	dialVia(t, h, alB, b, relayB, relayB)
	// Alice's first install, signed in at the first pair, reaches the
	// second pair's server through the first pair's relay origin.
	dialVia(t, h, alA, b, relayA, relayB)

	// A connector request is counted by the relay origin it came in on,
	// whatever server it is for.
	req, err := http.NewRequest(http.MethodPost, relayB+edgeproto.PathUniversalMCP, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.id+".not-a-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	m := h.relay().Metrics()
	if n := m.ConnectorRequests[relay.ConnectorKey{Origin: relayB, Protocol: edgeproto.Version}]; n != 1 {
		t.Fatalf("connector requests %v, want one through %s to a server on protocol %d", m.ConnectorRequests, relayB, edgeproto.Version)
	}
	if m.ServersByOrigin[relayA] != 1 || m.ServersByOrigin[relayB] != 1 {
		t.Fatalf("servers by origin %v, want one at %s and one at %s", m.ServersByOrigin, relayA, relayB)
	}
	if logins := h.node.svc.DeviceLogins(); logins[signinB] != 1 {
		t.Fatalf("device logins %v, want one at %s", logins, signinB)
	}
}

// dialVia dials s as c through via and checks that the grant the
// server accepted named enrolled, the relay origin s enrolled at.
func dialVia(t *testing.T, h *harness, c *client, s *serverNode, via, enrolled string) {
	t.Helper()
	from := h.proxy.mark()
	sc, err := h.dial(c, cli.Config{ServerID: s.id, EdgeURL: via})
	if err != nil {
		t.Fatalf("%s to server %s through %s: %v", c.user.Login, s.id, via, err)
	}
	_ = sc.Close()
	e := h.proxy.await(t, "the open to "+s.id, from, func(e logEntry) bool {
		_, ok := e.msg.(edgeproto.Open)
		return ok && e.fromEdge && e.serverID == s.id
	})
	open := e.msg.(edgeproto.Open)
	scope := edgeproto.GrantScope{Issuer: enrolled, ServerID: s.id, ConnID: open.ConnID, Kind: open.Kind}
	if _, err := edgeproto.VerifyGrant(h.edgeKey().Public().(ed25519.PublicKey), open.Grant, scope, time.Now()); err != nil {
		t.Fatalf("grant through %s: %v", via, err)
	}
}

// readyTo is the ready the edge sent s after from.
func (h *harness) readyTo(t *testing.T, s *serverNode, from int) edgeproto.Ready {
	t.Helper()
	e := h.proxy.await(t, "ready to "+s.id, from, func(e logEntry) bool {
		r, ok := e.msg.(edgeproto.Ready)
		return ok && e.fromEdge && r.ServerID == s.id
	})
	return e.msg.(edgeproto.Ready)
}
