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
	ViaConnector = store.AuditViaConnector
	ViaAssistant = "assistant"
)

// The names audit history gives what happens to a connector with no call
// behind it; none is an RPC. MethodConnectorReused is its revocation for
// presenting a rotated refresh token again, and MethodConnectorRemoved its
// deletion along with its member, or with the edge account that
// authorized it or its device, which the store records.
const (
	MethodConnectorCreate  = "connector.create"
	MethodConnectorReused  = "connector.reused"
	MethodConnectorRemoved = store.AuditConnectorRemoved
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

type auditChangeKey struct{}

// noteChange marks the call behind ctx as having changed a connector, an
// account share, a member or a Slack link, so its audit entry is kept
// among the security entries.
func noteChange(ctx context.Context) {
	if changed, ok := ctx.Value(auditChangeKey{}).(*bool); ok {
		*changed = true
	}
}

// auditCall records every write call by a connector or the assistant, and
// every call of auditedMethods; changed is whether the call noted a
// change. The entry holds the ids the params and result name, never the
// params themselves, and the workspace of the run or swarm it acted on.
// Recording happens after the call took effect, so a store failure is
// logged rather than turned into the call's error.
func (s *Server) auditCall(ctx context.Context, member domain.MemberID, method string, params, result json.RawMessage, perr *protocol.Error, changed bool) {
	var via, connector, client string
	switch id, ok := ctx.Value(connIdentityKey{}).(connIdentity); {
	case ok && id.connector != "":
		via, connector, client = ViaConnector, id.connector, id.connectorClient
	case ok:
		via = ViaCLI
	case fromAssistant(ctx):
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
		ID          string `json:"id"`
		ConnectorID string `json:"connector_id"`
		MemberID    string `json:"member_id"`
		DeviceID    string `json:"device_id"`
		ProposalID  string `json:"proposal_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	_ = json.Unmarshal(params, &ids)
	type made struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspace_id"`
	}
	var res struct {
		Run     made `json:"run"`
		Mission made `json:"mission"`
		Item    made `json:"item"`
		Merge   struct {
			AbsorbedID string `json:"absorbed_id"`
		} `json:"merge"`
		Link struct {
			UserID string `json:"user_id"`
		} `json:"link"`
	}
	_ = json.Unmarshal(result, &res)
	target := cmp.Or(res.Run.ID, res.Mission.ID, res.Item.ID, ids.RunID, ids.MissionID, ids.QuestionID, ids.ID)
	workspace := cmp.Or(res.Run.WorkspaceID, res.Mission.WorkspaceID, res.Item.WorkspaceID, ids.WorkspaceID)
	switch method {
	case protocol.MethodConnectorRevoke:
		target = ids.ConnectorID
	case protocol.MethodAccountShare, protocol.MethodAccountRevoke, protocol.MethodMemberRole, protocol.MethodMemberApprove,
		protocol.MethodMemberRemove, protocol.MethodMemberIdentityRemove:
		target = ids.MemberID
	case protocol.MethodMemberDeviceRevoke:
		target = ids.DeviceID
	case protocol.MethodMemberMergeAccept:
		target = cmp.Or(res.Merge.AbsorbedID, ids.ProposalID)
	case protocol.MethodSlackUnlink:
		target = res.Link.UserID
	}
	if workspace == "" && ids.RunID != "" {
		if r, err := s.cfg.Store.GetRun(ctx, domain.RunID(ids.RunID)); err == nil {
			workspace = string(r.WorkspaceID)
		}
	}
	if ms, ok := s.cfg.Store.(store.MissionStore); ok && workspace == "" && ids.MissionID != "" {
		if m, err := ms.GetMission(ctx, domain.MissionID(ids.MissionID)); err == nil {
			workspace = string(m.WorkspaceID)
		}
	}
	e := &store.AuditEntry{At: time.Now(), Member: member, Via: via, ConnectorID: connector, Client: client, Method: method,
		WorkspaceID: clipID(workspace), Target: clipID(target), Security: changed}
	if perr != nil {
		e.Code = perr.Code
	}
	s.recordAudit(ctx, e)
}

// RecordConnectorEvent records method, such as MethodConnectorCreate,
// happening to member's connector id of client.
func (s *Server) RecordConnectorEvent(ctx context.Context, method string, member domain.MemberID, id, client string) {
	s.recordAudit(ctx, &store.AuditEntry{At: time.Now(), Member: member, Via: ViaConnector, ConnectorID: id, Client: client,
		Method: method, Target: id, Security: true})
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
