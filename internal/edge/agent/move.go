package edgeagent

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coder/websocket"

	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// Move checks that target is another relay origin of the edge this server
// is enrolled with, and enrolls there once, so the server can take target
// as its edge-url. It returns target as a canonical origin. It writes no
// state: the edge key stays pinned, and the owner and claim code are kept
// per edge key. Each probe replaces the running server's control
// connection at the edge, which the running server dials again.
//
// It refuses unless the edge speaks edgeproto.VersionRelayOrigin or later
// and announces both origins: below that a server accepts connector
// tokens only for the origin it is enrolled at, so connectors registered
// at the current one would stop working.
func (a *Agent) Move(ctx context.Context, target string) (string, error) {
	to, err := edgeclient.Canonical(target)
	if err != nil {
		return "", fmt.Errorf("edgeagent: %w", err)
	}
	from, err := edgeclient.Canonical(a.origin)
	if err != nil {
		return "", fmt.Errorf("edgeagent: %w", err)
	}
	if to == from {
		return "", fmt.Errorf("edge-url already is %s", to)
	}
	pinned, err := a.state.PinnedKey()
	if err != nil {
		return "", err
	}
	if pinned == nil {
		return "", fmt.Errorf("no edge key is pinned for %s yet, so nothing shows that %s is the same edge; "+
			"start the server, let it connect once, and run edge move again", a.origin, to)
	}
	if err = a.probeMove(ctx, a.origin, from, to); err != nil {
		return "", err
	}
	info, err := FetchEdgeInfo(ctx, to)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(info.Key, pinned) {
		return "", fmt.Errorf("%s presents edge key %s, but this server pinned %s for its edge at %s, which announced it: another edge answers there. "+
			"edge move only changes the address of one edge; changing edges is in docs/edge.md#turning-it-on",
			to, edgeproto.EdgeKeyFingerprint(info.Key), edgeproto.EdgeKeyFingerprint(pinned), a.origin)
	}
	if err := a.probeMove(ctx, to, from, to); err != nil {
		return "", err
	}
	return to, nil
}

// probeMove enrolls at origin and checks that the edge answering there
// speaks edgeproto.VersionRelayOrigin or later and announces both from
// and to, all canonical: the edge at either address must, since a client
// may come in on either.
func (a *Agent) probeMove(ctx context.Context, origin, from, to string) error {
	ready, protocol, err := a.probe(ctx, origin)
	if err != nil {
		return err
	}
	if protocol < edgeproto.VersionRelayOrigin {
		return fmt.Errorf("%s speaks edge protocol %d; a server moved to %s would accept connector tokens only for that address, "+
			"so connectors registered at %s would stop working. Moving needs edge protocol %d or later: the edge's operator upgrades it",
			origin, protocol, to, from, edgeproto.VersionRelayOrigin)
	}
	announced := ready.RelayOrigins
	if len(announced) == 0 {
		announced = []string{origin}
	}
	canonical := make([]string, 0, len(announced))
	for _, o := range announced {
		if c, cerr := edgeclient.Canonical(o); cerr == nil {
			canonical = append(canonical, c)
		}
	}
	if !slices.Contains(canonical, from) || !slices.Contains(canonical, to) {
		return fmt.Errorf("the edge at %s announces the relay addresses %s; moving needs both %s and %s among them: the edge's operator adds the address first",
			origin, strings.Join(announced, ", "), from, to)
	}
	return nil
}

// probe enrolls at origin and closes the connection once the edge is
// ready.
func (a *Agent) probe(ctx context.Context, origin string) (edgeproto.Ready, int, error) {
	c, cancel, ready, protocol, err := a.enroll(ctx, origin)
	if err != nil {
		return edgeproto.Ready{}, 0, fmt.Errorf("edgeagent: enroll at %s: %w", origin, err)
	}
	defer cancel()
	_ = c.Close(websocket.StatusNormalClosure, "")
	return ready, protocol, nil
}
