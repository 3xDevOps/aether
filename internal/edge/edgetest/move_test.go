package edgetest

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// `aether-server edge move` moves a server enrolled at relay origin A to
// B, another origin of the same edge, while the server runs. After the
// restart that applies it, the server is online at B with the same edge
// key pinned and the same owner; a connector authorized at A before the
// move, a client signed in at A and a join link made at A still reach it,
// and a connector at B works too. Moving back works the same way.
func TestEdgeMove(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newConnectorClient(t)
	policy := edgeproto.PolicyAccount
	s := h.newConnectorServer(policy, client, sshd.Services{})
	al := h.login(alice)[0]
	h.claimServer(al, s)
	relayA := h.relayURL
	baseA := relayA + edgeproto.MCPPath(s.id)
	before := connectAt(t, h, client, al, s, baseA)
	bo := h.login(bob)[0]
	join := inviteLogin(t, h.control(al, s), bo, s.id, "viewer")
	pinned, err := s.state().PinnedKey()
	if err != nil || pinned == nil {
		t.Fatalf("pinned key %v, %v", pinned, err)
	}

	unserved := "http://127.0.0.3:9"
	_, err = moveAgent(t, s, relayA).Move(t.Context(), unserved)
	if want := fmt.Sprintf("the edge at %s announces the relay addresses %s; moving needs both %s and %s among them: the edge's operator adds the address first",
		relayA, relayA, relayA, unserved); err == nil || err.Error() != want {
		t.Errorf("move to an address the edge does not serve: %v, want %q", err, want)
	}
	_, err = moveAgent(t, s, relayA).Move(t.Context(), relayA)
	if want := "edge-url already is " + relayA; err == nil || err.Error() != want {
		t.Errorf("move to the address in use: %v, want %q", err, want)
	}
	// The refused move's probe replaced the server's control connection,
	// stating the policy the server has: the server is back at A as it was.
	backOnline(t, h, s)
	if m := h.relay().Metrics().ServersByOrigin; m[relayA] != 1 {
		t.Errorf("servers by origin after a refused move %v, want the server at %s", m, relayA)
	}
	srv, err := h.edgeStore().Server(t.Context(), s.id)
	if err != nil || srv.AccessPolicy != policy {
		t.Errorf("edge records policy %q (%v) after a refused move, want %s", srv.AccessPolicy, err, policy)
	}

	h.stopEdge()
	h.pairB = true
	h.startEdge()
	relayB := h.relayB
	backOnline(t, h, s)
	// The connector the Claude and ChatGPT listings name: the universal
	// URL at the address the server is leaving.
	universalA := universalAt(t, h, client, al, s, policy, h.signinURL, relayA)

	to, err := moveAgent(t, s, relayA).Move(t.Context(), relayB)
	if err != nil || to != relayB {
		t.Fatalf("move to %s: %q, %v", relayB, to, err)
	}
	// The probes replaced the running server's control connection; it
	// dials A again until restarted with the new edge-url.
	backOnline(t, h, s)
	s.edge = relayB
	s.restart(policy)
	backOnline(t, h, s)
	eventually(t, "the server enrolled at "+relayB, func() error {
		if m := h.relay().Metrics().ServersByOrigin; m[relayB] != 1 || m[relayA] != 0 {
			return fmt.Errorf("servers by origin %v", m)
		}
		return nil
	})
	st, _, err := s.state().Status()
	if err != nil || st.Edge != relayB {
		t.Errorf("status names edge %q (%v), want %s", st.Edge, err, relayB)
	}
	now, err := s.state().PinnedKey()
	if err != nil || !bytes.Equal(now, pinned) {
		t.Errorf("pinned key after the move %s (%v), want %s", edgeproto.EdgeKeyFingerprint(now), err, edgeproto.EdgeKeyFingerprint(pinned))
	}
	owner, err := s.state().Owner()
	if err != nil || owner == nil || owner.Login != alice.Login {
		t.Errorf("owner after the move %+v, %v", owner, err)
	}

	mustCall(t, baseA, before.AccessToken)
	mustCall(t, relayA+edgeproto.PathUniversalMCP, universalA.AccessToken)
	dialVia(t, h, al, s, relayA, relayB)
	if !strings.HasPrefix(join, relayA+"/") {
		t.Fatalf("join link %s is not at %s", join, relayA)
	}
	if _, err = h.joinBy(bo, join); err != nil {
		t.Fatalf("join link made at %s before the move: %v", relayA, err)
	}
	atB := connectAt(t, h, client, al, s, relayB+edgeproto.MCPPath(s.id))
	mustCall(t, baseA, before.AccessToken)

	to, err = moveAgent(t, s, relayB).Move(t.Context(), relayA)
	if err != nil || to != relayA {
		t.Fatalf("move back to %s: %q, %v", relayA, to, err)
	}
	backOnline(t, h, s)
	s.edge = relayA
	s.restart(policy)
	backOnline(t, h, s)
	mustCall(t, baseA, before.AccessToken)
	mustCall(t, relayB+edgeproto.MCPPath(s.id), atB.AccessToken)
	dialVia(t, h, al, s, relayA, relayA)
}

// A claim code issued before a move claims the server after it, through
// the address the server left: the code is kept per edge key, and the
// edge relays the claim to the server at its new address.
func TestEdgeMoveKeepsAPendingClaimCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.stopEdge()
	h.pairB = true
	h.startEdge()
	s := h.newServer(edgeproto.PolicyAccount)
	relayA, relayB := h.relayURL, h.relayB
	code := s.claimCode(t, time.Now())
	if _, err := moveAgent(t, s, relayA).Move(t.Context(), relayB); err != nil {
		t.Fatalf("move to %s: %v", relayB, err)
	}
	s.edge = relayB
	s.restart(edgeproto.PolicyAccount)
	res, err := h.claim(h.login(alice)[0], code)
	if err != nil || res.Config.ServerID != s.id {
		t.Fatalf("claim through %s of the server moved to %s: %+v, %v", relayA, relayB, res.Config, err)
	}
}

// Below edge protocol 6 a moved server would accept connector tokens only
// for its new address, so the move is refused.
func TestEdgeMoveRefusesBelowProtocol6(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
		if ch, ok := e.msg.(edgeproto.Challenge); ok {
			ch.Version = edgeproto.VersionRelayOrigin - 1
			return ch, true
		}
		return e.msg, true
	})
	s := h.newServer(edgeproto.PolicyAccount)
	h.claimServer(h.login(alice)[0], s)
	relayA := h.relayURL
	h.stopEdge()
	h.pairB = true
	h.startEdge()
	backOnline(t, h, s)
	_, err := moveAgent(t, s, relayA).Move(t.Context(), h.relayB)
	if want := fmt.Sprintf("%s speaks edge protocol 5; a server moved to %s would accept connector tokens only for that address, "+
		"so connectors registered at %s would stop working. Moving needs edge protocol 6 or later: the edge's operator upgrades it",
		relayA, h.relayB, relayA); err == nil || err.Error() != want {
		t.Errorf("move at protocol 5: %v, want %q", err, want)
	}
}

// An address the edge announces but another edge answers, as when its
// DNS name points elsewhere, is refused by the pinned key.
func TestEdgeMoveRefusesAnotherEdge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	other := newHarness(t)
	relayA := h.relayURL
	h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
		if r, ok := e.msg.(edgeproto.Ready); ok {
			r.RelayOrigins = []string{relayA, other.relayURL}
			return r, true
		}
		return e.msg, true
	})
	s := h.newServer(edgeproto.PolicyAccount)
	pinned, err := s.state().PinnedKey()
	if err != nil {
		t.Fatal(err)
	}
	_, err = moveAgent(t, s, relayA).Move(t.Context(), other.relayURL)
	want := fmt.Sprintf("%s presents edge key %s, but this server pinned %s for its edge at %s, which announced it: another edge answers there. "+
		"edge move only changes the address of one edge; changing edges is in docs/edge.md#turning-it-on",
		other.relayURL, edgeproto.EdgeKeyFingerprint(other.edgeKey().Public().(ed25519.PublicKey)), edgeproto.EdgeKeyFingerprint(pinned), relayA)
	if err == nil || err.Error() != want {
		t.Errorf("move to another edge: %v, want %q", err, want)
	}
	if now, err := s.state().PinnedKey(); err != nil || !bytes.Equal(now, pinned) {
		t.Errorf("pinned key after the refusal %s, %v", edgeproto.EdgeKeyFingerprint(now), err)
	}
	if other.relay().Online(s.id) {
		t.Error("the refused move enrolled the server at the other edge")
	}
}

// The edge must announce the address in use too, and an announced target
// that does not answer refuses the move after the probe at the current
// address: in both cases nothing changes and the server stays at A.
func TestEdgeMoveRefusesAMissingOrUnreachableAddress(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.newServer(edgeproto.PolicyAccount)
	h.claimServer(h.login(alice)[0], s)
	relayA := h.relayURL
	const elsewhere, unreachable = "http://127.0.0.1:2", "http://127.0.0.1:1"
	for _, tc := range []struct {
		announced []string
		want      func(error) bool
		wantText  string
	}{
		{[]string{elsewhere, unreachable}, nil, fmt.Sprintf("the edge at %s announces the relay addresses %s, %s; moving needs both %s and %s among them: "+
			"the edge's operator adds the address first", relayA, elsewhere, unreachable, relayA, unreachable)},
		{[]string{relayA, unreachable}, func(err error) bool {
			return err != nil && strings.HasPrefix(err.Error(), `edgeagent: Get "`+unreachable+`/v1/edge": `)
		}, ""},
	} {
		h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
			if r, ok := e.msg.(edgeproto.Ready); ok {
				r.RelayOrigins = tc.announced
				return r, true
			}
			return e.msg, true
		})
		_, err := moveAgent(t, s, relayA).Move(t.Context(), unreachable)
		if tc.want != nil && !tc.want(err) || tc.want == nil && (err == nil || err.Error() != tc.wantText) {
			t.Errorf("announced %v: %v, want %q", tc.announced, err, tc.wantText)
		}
		backOnline(t, h, s)
		if st, _, err := s.state().Status(); err != nil || st.Edge != relayA {
			t.Errorf("announced %v: status names edge %q (%v), want %s", tc.announced, st.Edge, err, relayA)
		}
	}
}

// The edge answering at the target must pass the same checks as the one
// at the current address: a target that speaks protocol 5, or announces
// only itself, refuses the move after the probe there.
func TestEdgeMoveChecksTheTargetToo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.newServer(edgeproto.PolicyAccount)
	h.claimServer(h.login(alice)[0], s)
	relayA := h.relayURL
	h.stopEdge()
	h.pairB = true
	h.startEdge()
	backOnline(t, h, s)
	relayB := h.relayB
	hostB := strings.TrimPrefix(relayB, "http://")
	for _, tc := range []struct {
		name   string
		tamper func(edgeproto.Message) edgeproto.Message
		want   string
	}{
		{"protocol 5 at the target", func(m edgeproto.Message) edgeproto.Message {
			if ch, ok := m.(edgeproto.Challenge); ok {
				ch.Version = edgeproto.VersionRelayOrigin - 1
				return ch
			}
			return m
		}, fmt.Sprintf("%s speaks edge protocol 5; a server moved to %s would accept connector tokens only for that address, "+
			"so connectors registered at %s would stop working. Moving needs edge protocol 6 or later: the edge's operator upgrades it", relayB, relayB, relayA)},
		{"the target announces only itself", func(m edgeproto.Message) edgeproto.Message {
			if r, ok := m.(edgeproto.Ready); ok {
				r.RelayOrigins = []string{relayB}
				return r
			}
			return m
		}, fmt.Sprintf("the edge at %s announces the relay addresses %s; moving needs both %s and %s among them: the edge's operator adds the address first",
			relayB, relayB, relayA, relayB)},
	} {
		h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
			if e.link.host != hostB {
				return e.msg, true
			}
			return tc.tamper(e.msg), true
		})
		if _, err := moveAgent(t, s, relayA).Move(t.Context(), relayB); err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
		backOnline(t, h, s)
	}
}

// moveAgent is the agent `aether-server edge move` builds over s's data
// directory, with edge-url from and the server's edge-access.
func moveAgent(t *testing.T, s *serverNode, from string) *edgeagent.Agent {
	t.Helper()
	hostKey, err := sshd.LoadOrCreateHostKey(filepath.Join(s.dir, "ssh", "host_ed25519_key"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := edgeagent.New(edgeagent.Config{EdgeURL: from, DataDir: s.dir, HostKey: hostKey, AccessPolicy: s.policy})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
