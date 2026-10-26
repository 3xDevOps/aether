package sshd

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodAuditList, (*Server).auditList)
}

// How a call arrived, as audit entries record it.
const (
	ViaDashboard = "dashboard"
	ViaCLI       = "cli"
	ViaConnector = "connector"
	ViaAssistant = "assistant"
)

// The names audit history gives what happens to a connector with no call
// behind it; none is an RPC. MethodConnectorReused is its revocation for
// presenting a rotated refresh token again, and MethodConnectorRemoved its
// deletion along with its member or with the edge account its device
// signed in with.
const (
	MethodConnectorCreate  = "connector.create"
	MethodConnectorReused  = "connector.reused"
	MethodConnectorRemoved = "connector.removed"
)

const (
	auditTimeout   = 5 * time.Second
	auditTargetMax = 128
)

// auditedMethods are audited whoever calls them.
var auditedMethods = map[string]bool{
	protocol.MethodConnectorRevoke:      true,
	protocol.MethodAccountShare:         true,
	protocol.MethodAccountRevoke:        true,
	protocol.MethodMemberRole:           true,
	protocol.MethodMemberApprove:        true,
	protocol.MethodMemberRemove:         true,
	protocol.MethodMemberDeviceRevoke:   true,
	protocol.MethodMemberIdentityRemove: true,
	protocol.MethodMemberMergeAccept:    true,
	protocol.MethodSlackLink:            true,
	protocol.MethodSlackUnlink:          true,
}

// assistantReads are the read-only methods the assistant calls that no
// connector may; with connectorMethodScopes' reads they are not audited.
var assistantReads = map[string]bool{
	protocol.MethodMemberList:     true,
	protocol.MethodTerminalStatus: true,
}

type assistantCallKey struct{}

// AssistantLocal is Local for member's built-in assistant: its calls are
// the member's own, and audit history records the writes among them.
func (s *Server) AssistantLocal(member domain.MemberID) *Local {
	return &Local{s: s, member: member, assistant: true}
}

// auditCall records every write call by a connector or the assistant, and
// every call of auditedMethods. The entry holds the ids the params and
// result name, never the params themselves. Recording happens after the
// call took effect, so a store failure is logged rather than turned into
// the call's error.
func (s *Server) auditCall(ctx context.Context, member domain.MemberID, method string, params, result json.RawMessage, perr *protocol.Error) {
	var via, connector, client string
	switch id, ok := ctx.Value(connIdentityKey{}).(connIdentity); {
	case ok && id.connector != "":
		via, connector, client = ViaConnector, id.connector, id.connectorClient
	case ok:
		via = ViaCLI
	case ctx.Value(assistantCallKey{}) == true:
		via = ViaAssistant
	default:
		via = ViaDashboard
	}
	scopes, listed := connectorMethodScopes[method]
	write := (!listed || !slices.Equal(scopes, scopeRead)) && !assistantReads[method]
	if !auditedMethods[method] && (!write || via == ViaDashboard || via == ViaCLI) {
		return
	}
	var ids struct {
		RunID       string `json:"run_id"`
		MissionID   string `json:"mission_id"`
		QuestionID  string `json:"question_id"`
		ConnectorID string `json:"connector_id"`
		MemberID    string `json:"member_id"`
		DeviceID    string `json:"device_id"`
		ProposalID  string `json:"proposal_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	_ = json.Unmarshal(params, &ids)
	var made struct {
		Run     struct{ ID string } `json:"run"`
		Mission struct{ ID string } `json:"mission"`
		Merge   struct {
			AbsorbedID string `json:"absorbed_id"`
		} `json:"merge"`
		Link struct {
			UserID string `json:"user_id"`
		} `json:"link"`
	}
	_ = json.Unmarshal(result, &made)
	target := cmp.Or(made.Run.ID, made.Mission.ID, ids.RunID, ids.MissionID, ids.QuestionID)
	switch method {
	case protocol.MethodConnectorRevoke:
		target = ids.ConnectorID
	case protocol.MethodAccountShare, protocol.MethodAccountRevoke, protocol.MethodMemberRole, protocol.MethodMemberApprove,
		protocol.MethodMemberRemove, protocol.MethodMemberIdentityRemove:
		target = ids.MemberID
	case protocol.MethodMemberDeviceRevoke:
		target = ids.DeviceID
	case protocol.MethodMemberMergeAccept:
		target = cmp.Or(made.Merge.AbsorbedID, ids.ProposalID)
	case protocol.MethodSlackUnlink:
		target = made.Link.UserID
	}
	e := &store.AuditEntry{At: time.Now(), Member: member, Via: via, ConnectorID: connector, Client: client, Method: method,
		WorkspaceID: clipID(ids.WorkspaceID), Target: clipID(target)}
	if perr != nil {
		e.Code = perr.Code
	}
	s.recordAudit(ctx, e)
}

// RecordConnectorEvent records method, such as MethodConnectorCreate,
// happening to member's connector id of client.
func (s *Server) RecordConnectorEvent(ctx context.Context, method string, member domain.MemberID, id, client string) {
	s.recordAudit(ctx, &store.AuditEntry{At: time.Now(), Member: member, Via: ViaConnector, ConnectorID: id, Client: client,
		Method: method, Target: id})
}

// connectorsOf lists member's connectors so that removing the member or
// a device deletes them on the record; a store without connectors has none.
func (s *Server) connectorsOf(ctx context.Context, member domain.MemberID) ([]*store.Connector, error) {
	cs, ok := s.cfg.Store.(store.ConnectorStore)
	if !ok {
		return nil, nil
	}
	return cs.ListConnectors(ctx, member, time.Now())
}

// recordConnectorsRemoved records the deletion of each of conns.
func (s *Server) recordConnectorsRemoved(ctx context.Context, conns []*store.Connector) {
	for _, c := range conns {
		s.RecordConnectorEvent(ctx, MethodConnectorRemoved, c.Member, c.ID, c.ClientName)
	}
}

func (s *Server) recordAudit(ctx context.Context, e *store.AuditEntry) {
	as, ok := s.cfg.Store.(store.AuditStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	if err := as.AddAudit(ctx, e); err != nil {
		slog.Error("sshd: audit entry not recorded", "member", e.Member, "via", e.Via, "connector", e.ConnectorID,
			"method", e.Method, "target", e.Target, "code", e.Code, "error", err)
	}
}

func clipID(id string) string {
	if len(id) > auditTargetMax {
		return id[:auditTargetMax]
	}
	return id
}

func (s *Server) auditList(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AuditListParams](raw)
	if perr != nil {
		return nil, perr
	}
	switch {
	case p.Limit < 0 || p.Limit > protocol.AuditListMaxLimit:
		return nil, invalidParams("limit must be between 0 and 200; 0 means 50")
	case p.Before < 0:
		return nil, invalidParams("before must be a next_before from audit.list")
	}
	as, ok := s.cfg.Store.(store.AuditStore)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the store does not keep audit history"}
	}
	m, err := s.memberFor(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	q := store.AuditQuery{ConnectorID: p.ConnectorID, Before: p.Before, Limit: cmp.Or(p.Limit, protocol.AuditListDefaultLimit)}
	if m.Role != domain.RoleAdmin {
		q.Member = member
	}
	entries, err := as.ListAudit(ctx, q)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.AuditListResult{Entries: make([]protocol.AuditEntry, 0, len(entries))}
	for _, e := range entries {
		w := protocol.AuditEntry{Seq: e.Seq, At: e.At.UTC().Format(time.RFC3339), MemberID: string(e.Member), Via: e.Via,
			ConnectorID: e.ConnectorID, Client: e.Client, Method: e.Method, WorkspaceID: e.WorkspaceID, Target: e.Target,
			Outcome: "ok", Code: e.Code}
		if e.Code != 0 {
			w.Outcome = "error"
		}
		out.Entries = append(out.Entries, w)
	}
	if len(entries) == q.Limit {
		out.NextBefore = entries[len(entries)-1].Seq
	}
	return out, nil
}
