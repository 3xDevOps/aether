package sshd

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
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

	protocol.MethodWorkspaceAccessGrant:  true,
	protocol.MethodWorkspaceAccessRevoke: true,
	protocol.MethodWorkspaceOwnerSet:     true,
}

// assistantReads are the read-only methods the assistant calls that no
// connector may; with connectorMethodScopes' reads they are not audited.
var assistantReads = map[string]bool{
	protocol.MethodMemberList:     true,
	protocol.MethodTerminalStatus: true,
}

type auditNoteKey struct{}

// auditNote is what the call behind a dispatch learns for its audit entry:
// whether it changed something, and the first workspace the server
// resolved for it.
type auditNote struct {
	changed   bool
	workspace domain.WorkspaceID
}

// noteChange marks the call behind ctx as having changed a connector, an
// account share, a member or a Slack link, so its audit entry is kept
// among the security entries.
func noteChange(ctx context.Context) {
	if note, ok := ctx.Value(auditNoteKey{}).(*auditNote); ok {
		note.changed = true
	}
}

// noteWorkspace records ws as the workspace the call behind ctx acts in,
// unless one was resolved before it.
func noteWorkspace(ctx context.Context, ws domain.WorkspaceID) {
	if note, ok := ctx.Value(auditNoteKey{}).(*auditNote); ok && note.workspace == "" {
		note.workspace = ws
	}
}

// auditCall records every write call by a connector or the assistant, and
// every call of auditedMethods; changed is whether the call noted a
// change. The entry holds the ids the params and result name, never the
// params themselves, and the workspace the server resolved for the call:
// the result's, the guard's or an in-handler check's target, or the named
// run's, kept only when the caller reaches it.
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
		ID          string `json:"id"`
		ConnectorID string `json:"connector_id"`
		MemberID    string `json:"member_id"`
		DeviceID    string `json:"device_id"`
		ProposalID  string `json:"proposal_id"`
	}
	_ = json.Unmarshal(params, &ids)
	type made struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspace_id"`
	}
	var res struct {
		Run   made `json:"run"`
		Item  made `json:"item"`
		Merge struct {
			AbsorbedID string `json:"absorbed_id"`
		} `json:"merge"`
		Link struct {
			UserID string `json:"user_id"`
		} `json:"link"`
	}
	_ = json.Unmarshal(result, &res)
	target := cmp.Or(res.Run.ID, res.Item.ID, ids.RunID, ids.ID)
	workspace := cmp.Or(res.Run.WorkspaceID, res.Item.WorkspaceID)
	if note, ok := ctx.Value(auditNoteKey{}).(*auditNote); ok && workspace == "" {
		workspace = string(note.workspace)
	}
	switch method {
	case protocol.MethodConnectorRevoke:
		target = ids.ConnectorID
	case protocol.MethodAccountShare, protocol.MethodAccountRevoke, protocol.MethodMemberRole, protocol.MethodMemberApprove,
		protocol.MethodMemberRemove, protocol.MethodMemberIdentityRemove, protocol.MethodWorkspaceAccessRevoke, protocol.MethodWorkspaceOwnerSet:
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
	if workspace != "" {
		if actor, err := resolveActor(ctx, s.cfg.Store, member); err != nil || !actor.Reaches(domain.WorkspaceID(workspace)) {
			workspace = ""
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
	limit := cmp.Or(p.Limit, protocol.AuditListDefaultLimit)
	q := store.AuditQuery{ConnectorID: p.ConnectorID, Before: p.Before, Limit: limit}
	actor := permissions.ActorOf(m)
	shows := func(*store.AuditEntry) bool { return true }
	if permissions.Check(permissions.ServerAdmin, actor, permissions.Target{}) != nil {
		q.Member = member
		for ws := range m.Access.Owned {
			if workspaceAdmin(actor, ws) {
				q.Workspaces = append(q.Workspaces, ws)
			}
		}
		runs := newRunScopes(s.cfg.Store)
		notes, _ := s.cfg.Store.(interface {
			GetMemory(ctx context.Context, workspace domain.WorkspaceID, id string, workspaces []domain.WorkspaceID) (*store.Memory, []*store.Memory, error)
		})
		// Another member's entry about a session, or about a note from
		// one, is shown only to a reader who reaches every workspace of
		// that session; one about a deleted session only to super admins.
		// A deleted note's session is unknown, so its entries stay shown.
		shows = func(e *store.AuditEntry) bool {
			switch {
			case e.Member == member:
				return true
			case strings.HasPrefix(e.Target, "run-"):
				return runs.reaches(ctx, actor, domain.RunID(e.Target))
			case strings.HasPrefix(e.Method, "memory.") && e.Target != "":
				if notes == nil {
					return false
				}
				note, _, err := notes.GetMemory(ctx, domain.WorkspaceID(e.WorkspaceID), e.Target, nil)
				if errors.Is(err, store.ErrNotFound) {
					return true
				}
				return err == nil && (note.RunID == "" || runs.reaches(ctx, actor, note.RunID))
			}
			return true
		}
	}
	var entries []*store.AuditEntry
	for len(entries) < limit {
		page, err := as.ListAudit(ctx, q)
		if err != nil {
			return nil, rpcError(err)
		}
		for _, e := range page {
			if len(entries) < limit && shows(e) {
				entries = append(entries, e)
			}
		}
		if len(page) < q.Limit {
			break
		}
		q.Before = page[len(page)-1].Seq
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
	if len(entries) == limit {
		out.NextBefore = entries[len(entries)-1].Seq
	}
	return out, nil
}
