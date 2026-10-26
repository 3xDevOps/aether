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

// MethodConnectorCreate names a connector's authorization in audit
// history; it is no RPC.
const MethodConnectorCreate = "connector.create"

const (
	auditTimeout   = 5 * time.Second
	auditTargetMax = 128
)

// auditedMethods are audited whoever calls them.
var auditedMethods = map[string]bool{
	protocol.MethodConnectorRevoke:   true,
	protocol.MethodAccountShare:      true,
	protocol.MethodAccountRevoke:     true,
	protocol.MethodMemberRole:        true,
	protocol.MethodMemberMergeAccept: true,
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

// callVia is how the call behind ctx arrived, and the connector and its
// client for a connector call.
func callVia(ctx context.Context) (via, connector, client string) {
	if id, ok := ctx.Value(connIdentityKey{}).(connIdentity); ok {
		if id.connector != "" {
			return ViaConnector, id.connector, id.connectorClient
		}
		return ViaCLI, "", ""
	}
	if assistant, _ := ctx.Value(assistantCallKey{}).(bool); assistant {
		return ViaAssistant, "", ""
	}
	return ViaDashboard, "", ""
}

// audits reports whether a call of method that arrived via is audited:
// every write by a connector or the assistant, and auditedMethods.
func audits(via, method string) bool {
	if auditedMethods[method] {
		return true
	}
	if via != ViaConnector && via != ViaAssistant {
		return false
	}
	scopes, listed := connectorMethodScopes[method]
	return (!listed || !slices.Equal(scopes, scopeRead)) && !assistantReads[method]
}

// auditCall records a call dispatch decided to audit. The entry holds the
// ids the params and result name, never the params themselves. Recording
// happens after the call took effect, so a store failure is logged rather
// than turned into the call's error.
func (s *Server) auditCall(ctx context.Context, member domain.MemberID, method string, params, result json.RawMessage, perr *protocol.Error) {
	via, connector, client := callVia(ctx)
	if !audits(via, method) {
		return
	}
	var ids struct {
		RunID       string `json:"run_id"`
		MissionID   string `json:"mission_id"`
		QuestionID  string `json:"question_id"`
		ConnectorID string `json:"connector_id"`
		MemberID    string `json:"member_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	_ = json.Unmarshal(params, &ids)
	var made struct {
		Run     struct{ ID string } `json:"run"`
		Mission struct{ ID string } `json:"mission"`
	}
	_ = json.Unmarshal(result, &made)
	target := cmp.Or(ids.RunID, ids.MissionID, ids.QuestionID, made.Run.ID, made.Mission.ID)
	switch method {
	case protocol.MethodConnectorRevoke:
		target = ids.ConnectorID
	case protocol.MethodAccountShare, protocol.MethodAccountRevoke, protocol.MethodMemberRole:
		target = ids.MemberID
	}
	e := &store.AuditEntry{At: time.Now(), Member: member, Via: via, ConnectorID: connector, Client: client, Method: method,
		WorkspaceID: clipID(ids.WorkspaceID), Target: clipID(target)}
	if perr != nil {
		e.Code = perr.Code
	}
	s.recordAudit(ctx, e)
}

// RecordConnectorCreated records that member authorized connector id for
// client.
func (s *Server) RecordConnectorCreated(ctx context.Context, member domain.MemberID, id, client string) {
	s.recordAudit(ctx, &store.AuditEntry{At: time.Now(), Member: member, Via: ViaConnector, ConnectorID: id, Client: client,
		Method: MethodConnectorCreate, Target: id})
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
