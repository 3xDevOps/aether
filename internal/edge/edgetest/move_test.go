package edgetest

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

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

	h.stopEdge()
	h.pairB = true
	h.startEdge()
	relayB := h.relayB
	backOnline(t, h, s)

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
	s.edge = relayA
	s.restart(policy)
	backOnline(t, h, s)
	mustCall(t, baseA, before.AccessToken)
	mustCall(t, relayB+edgeproto.MCPPath(s.id), atB.AccessToken)
	dialVia(t, h, al, s, relayA, relayA)
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

// moveAgent is the agent `aether-server edge move` builds over s's data
// directory, with edge-url from.
func moveAgent(t *testing.T, s *serverNode, from string) *edgeagent.Agent {
	t.Helper()
	hostKey, err := sshd.LoadOrCreateHostKey(filepath.Join(s.dir, "ssh", "host_ed25519_key"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := edgeagent.New(edgeagent.Config{EdgeURL: from, DataDir: s.dir, HostKey: hostKey})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
