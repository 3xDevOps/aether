package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// The layers of the path to a server through an edge, as EdgePathError
// names the one that failed.
const (
	// LayerClientEdge is this machine reaching the edge: DNS, TCP, TLS, a
	// proxy, the edge's protocol version, or its rate limits.
	LayerClientEdge = "client-edge"
	// LayerAuthorization is this device's sign-in on the edge and its
	// standing on the server: membership, approval, revocation.
	LayerAuthorization = "authorization"
	// LayerRegistration is the server's own control connection to the edge.
	LayerRegistration = "server-registration"
	// LayerEndToEnd is the relayed connection to the server itself, once
	// the edge opened it.
	LayerEndToEnd = "end-to-end"
)

// serversLookupTimeout bounds the server list a registration failure
// reads to name the servers the edge does see online.
const serversLookupTimeout = 10 * time.Second

// EdgePathError is a failure to reach ServerID through the edge at Edge,
// a host name, with the layer that failed. Account is the account this
// device signed in to that edge as, empty when it has none. Online lists
// the other servers the account reaches that the edge sees online, read
// only when the edge holds no live registration for ServerID: a server
// that was reinstalled has a new id, and the link still names the old one.
type EdgePathError struct {
	Layer    string
	ServerID string
	Edge     string
	Account  string
	Online   []edgeproto.ServerInfo
	Err      error
}

func (e *EdgePathError) Error() string {
	msg := e.Err.Error()
	switch {
	case e.Layer != LayerRegistration:
		return msg
	case len(e.Online) == 0:
		return msg + "\n  on the server, sudo aether-server edge status shows its id and its connection to " + e.Edge
	}
	ids := make([]string, len(e.Online))
	for i, s := range e.Online {
		ids[i] = fmt.Sprintf("%s (%s)", s.ID, s.Name)
	}
	return fmt.Sprintf("%s\n  this link names server %s, but %s sees these servers of %s online: %s\n"+
		"  if one is this server under a new id, link it with: aether link --from-edge <id>",
		msg, e.ServerID, e.Edge, e.Account, strings.Join(ids, ", "))
}

func (e *EdgePathError) Unwrap() error { return e.Err }

// edgeFailure is err, a failure of layer on the way to serverID through
// edge, as the device signed in with session, nil before it has one.
func edgeFailure(edge *edgeclient.Client, serverID string, session *edgeclient.Session, layer string, err error) *EdgePathError {
	fail := &EdgePathError{Layer: layer, ServerID: serverID, Edge: edge.Host(), Err: err}
	if session != nil {
		a := session.Account.Account
		fail.Account = a.Provider + " account " + cmp.Or(a.Login, a.Subject)
	}
	return fail
}

// relayLayer is the layer of a failure to open the relayed connection.
// The edge refuses for the server's registration, for this device's
// sign-in or membership, or for its own limits; a failure without an
// answer, or an answer from a proxy in between, is the way to the edge.
func relayLayer(err error) string {
	var refused *edgeclient.RefusedError
	if !errors.As(err, &refused) || !refused.FromEdge {
		return LayerClientEdge
	}
	r := edgeproto.Refusal(refused.Message)
	switch edgeproto.RefusedBy(r).Kind {
	case edgeproto.RefusedNotConnected, edgeproto.RefusedNotClaimed, edgeproto.RefusedUnknownServer:
		return LayerRegistration
	case edgeproto.RefusedTooMany, edgeproto.RefusedConnLimit:
		return LayerClientEdge
	}
	switch {
	case r == edgeproto.RefusalNotAttached:
		return LayerRegistration
	case refused.Status >= http.StatusInternalServerError:
		return LayerClientEdge
	}
	return LayerAuthorization
}

// handshakeLayer is the layer of a failed SSH handshake over an open
// relayed connection: the server's refusal is authorization, anything
// else the connection to the server.
func handshakeLayer(err error) string {
	var (
		refused *edgeproto.Refused
		said    *refusalError
	)
	if errors.As(err, &refused) || errors.As(err, &said) {
		return LayerAuthorization
	}
	return LayerEndToEnd
}

// onlineServers is the servers other than serverID that edge sees online
// for its signed-in account, nil when it cannot say.
func onlineServers(ctx context.Context, edge *edgeclient.Client, serverID string) []edgeproto.ServerInfo {
	ctx, cancel := context.WithTimeout(ctx, serversLookupTimeout)
	defer cancel()
	servers, err := edge.Servers(ctx)
	if err != nil {
		return nil
	}
	var online []edgeproto.ServerInfo
	for _, s := range servers {
		if s.Online && s.ID != serverID {
			online = append(online, s)
		}
	}
	return online
}
