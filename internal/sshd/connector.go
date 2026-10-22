package sshd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodConnectorCode, (*Server).connectorCode)
	registerMethod(protocol.MethodConnectorList, (*Server).connectorList)
	registerMethod(protocol.MethodConnectorRevoke, (*Server).connectorRevoke)
}

// ConnectorCodeTTL is how long a connector pairing code lives.
const ConnectorCodeTTL = 10 * time.Minute

// ErrConnectorCode means a pairing code is no live code: wrong, expired
// or spent.
var ErrConnectorCode = errors.New("sshd: no such connector code")

// ConnectorGrant is whom a pairing code authorizes a connector for: the
// member who minted it and the edge device they minted it on, empty over
// a member SSH key or a tailnet connection.
type ConnectorGrant struct {
	Member domain.MemberID
	Device domain.DeviceID
}

// connectorCode is a live pairing code, kept as the SHA-256 of its
// normalized form and never stored.
type connectorCode struct {
	grant   ConnectorGrant
	expires time.Time
}

// ConnectorLabel names a connector in audit text and errors: ChatGPT
// connector con-xxx.
func ConnectorLabel(client, id string) string {
	return client + " connector " + id
}

// ConnectorURL is the URL members add as a custom connector,
// <edge origin>/mcp/<server id>, or empty while the server has no edge.
func (s *Server) ConnectorURL() string {
	edge := s.currentEdgeOwner()
	if edge == nil {
		return ""
	}
	return edge.Origin() + edgeproto.MCPPath(edgeproto.ServerID(s.hostKey.PublicKey()))
}

// Name is the server's host name.
func (s *Server) Name() string { return s.cfg.Name }

func (s *Server) connectorStore() (store.ConnectorStore, *protocol.Error) {
	cs, ok := s.cfg.Store.(store.ConnectorStore)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the store does not keep connectors"}
	}
	return cs, nil
}

func connectorCodeHash(code string) string {
	sum := sha256.Sum256([]byte(store.NormalizeApprovalCode(code)))
	return hex.EncodeToString(sum[:])
}

// connectorCode mints the caller a pairing code, replacing any code of
// theirs not yet spent, so a member holds at most one.
func (s *Server) connectorCode(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodConnectorCode
	if perr := s.requireApprovedCaller(ctx, method, false); perr != nil {
		return nil, perr
	}
	if _, perr := s.connectorStore(); perr != nil {
		return nil, perr
	}
	edge := s.currentEdgeOwner()
	if edge == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf(
			"%s: this server has no edge (edge-url is empty), and ChatGPT and Claude reach a connector only through one; "+
				"an admin turns it on with: sudo aether-server config set edge-url %s, then sudo systemctl restart aether-server",
			method, edgeclient.DefaultURL)}
	}
	switch version := edge.EdgeVersion(); {
	case version == 0:
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf(
			"%s: this server is not connected to its edge at %s; an admin sees why with: sudo aether-server edge status",
			method, edge.Origin())}
	case version < edgeproto.VersionHTTP:
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf(
			"%s: edge %s speaks edge protocol %d and cannot forward connectors; its operator upgrades it",
			method, edge.Origin(), version)}
	}
	code, err := store.RandomApprovalCode()
	if err != nil {
		return nil, rpcError(err)
	}
	id, _ := ctx.Value(connIdentityKey{}).(connIdentity)
	now := time.Now()
	expires := now.Add(ConnectorCodeTTL)
	s.connectorMu.Lock()
	if s.connectorCodes == nil {
		s.connectorCodes = make(map[string]connectorCode)
	}
	maps.DeleteFunc(s.connectorCodes, func(_ string, c connectorCode) bool {
		return c.grant.Member == member || !now.Before(c.expires)
	})
	s.connectorCodes[connectorCodeHash(code)] = connectorCode{grant: ConnectorGrant{Member: member, Device: id.device}, expires: expires}
	s.connectorMu.Unlock()
	slog.Info("sshd: connector code minted", "member", member, "device", id.device)
	return protocol.ConnectorCodeResult{URL: s.ConnectorURL(), Code: code, ExpiresAt: expires.UTC().Format(time.RFC3339)}, nil
}

// RedeemConnectorCode spends code, as a person typed it: any case, the
// dash optional. It is ErrConnectorCode unless code is live at now.
func (s *Server) RedeemConnectorCode(code string, now time.Time) (ConnectorGrant, error) {
	hash := connectorCodeHash(code)
	s.connectorMu.Lock()
	defer s.connectorMu.Unlock()
	c, ok := s.connectorCodes[hash]
	if !ok {
		return ConnectorGrant{}, ErrConnectorCode
	}
	delete(s.connectorCodes, hash)
	if !now.Before(c.expires) {
		return ConnectorGrant{}, ErrConnectorCode
	}
	return c.grant, nil
}

// CheckConnectorGrant re-checks a grant as each connector request does:
// the member still exists and, when the code was minted on an edge
// device, that device still admits a connection under the current
// edge-access policy. A revoked device never does; under approved-devices
// only an approved one does.
func (s *Server) CheckConnectorGrant(ctx context.Context, g ConnectorGrant) error {
	return s.checkConnIdentity(ctx, connIdentity{member: g.Member, device: g.Device})
}

func connectorToWire(c *store.Connector) protocol.Connector {
	out := protocol.Connector{ID: c.ID, MemberID: string(c.Member), ClientName: c.ClientName, RedirectHost: c.RedirectHost,
		DeviceID: string(c.Device), Scopes: c.Scopes, CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339)}
	if c.LastUsedAt != nil {
		out.LastUsedAt = c.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (s *Server) connectorList(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	cs, perr := s.connectorStore()
	if perr != nil {
		return nil, perr
	}
	list, err := cs.ListConnectors(ctx, member, time.Now())
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.ConnectorListResult{Connectors: make([]protocol.Connector, 0, len(list))}
	for _, c := range list {
		out.Connectors = append(out.Connectors, connectorToWire(c))
	}
	return out, nil
}

func (s *Server) connectorRevoke(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodConnectorRevoke
	p, perr := decodeParams[protocol.ConnectorRevokeParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.ConnectorID == "" {
		return nil, invalidParams("connector_id is required")
	}
	cs, perr := s.connectorStore()
	if perr != nil {
		return nil, perr
	}
	c, err := cs.GetConnector(ctx, p.ConnectorID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf(
			"%s: no connector %s; aether mcp lists yours", method, p.ConnectorID)}
	}
	if err != nil {
		return nil, rpcError(err)
	}
	if perr := s.actingOn(ctx, member, c.Member, method); perr != nil {
		return nil, perr
	}
	if err := cs.DeleteConnector(ctx, c.ID); err != nil {
		return nil, rpcError(err)
	}
	slog.Info("sshd: connector revoked", "actor", member, "connector", c.ID, "member", c.Member)
	return protocol.ConnectorRevokeResult{Connector: connectorToWire(c)}, nil
}
