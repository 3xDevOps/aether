package sshd

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerGuarded(protocol.MethodAttentionList, permissions.View, nil, (*Server).attentionList)
}

// attentionRun is what the dashboard's Needs you rows read about one run
// (StateContext in web/src/lib/needs-you.ts), gathered on the server.
type attentionRun struct {
	run        *domain.Run
	paused     bool
	controller domain.MemberID
	inputs     []domain.RunInputRequest
	pending    []acphost.Request
	// open are the run's open room questions and queued steer requests,
	// oldest first, read only when a room row can hold.
	open []*store.RoomMessage
}

type attentionViewer struct {
	id    domain.MemberID
	admin bool
	name  func(domain.MemberID) string
}

// attentionList is the server port of the dashboard's Needs you rows
// (needsYouConditions in web/src/lib/needs-you.ts): each run listed once,
// under the first row that holds for the caller, worded as that row is.
func (s *Server) attentionList(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AttentionListParams](raw)
	if perr != nil {
		return nil, perr
	}
	caller, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	var workspaces []*domain.Workspace
	if p.WorkspaceID != "" {
		ws, getErr := s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(p.WorkspaceID))
		if getErr != nil {
			return nil, rpcError(getErr)
		}
		workspaces = []*domain.Workspace{ws}
	} else if workspaces, err = s.cfg.Store.ListWorkspaces(ctx); err != nil {
		return nil, rpcError(err)
	}
	names, err := s.memberNames(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	viewer := attentionViewer{id: member, admin: caller.Role == domain.RoleAdmin, name: names}
	items := []protocol.AttentionItem{}
	for _, ws := range workspaces {
		runs, err := s.cfg.Store.ListRunsByWorkspace(ctx, ws.ID)
		if err != nil {
			return nil, rpcError(err)
		}
		for _, run := range runs {
			if run.ArchivedAt != nil {
				continue
			}
			r, err := s.attentionRun(ctx, run)
			if err != nil {
				return nil, rpcError(err)
			}
			if item, ok := attentionItem(r, viewer); ok {
				items = append(items, item)
			}
		}
	}
	slices.SortStableFunc(items, func(a, b protocol.AttentionItem) int {
		return cmp.Or(compareSince(a.Since, b.Since), cmp.Compare(a.RunID, b.RunID))
	})
	return protocol.AttentionListResult{Items: items}, nil
}

// compareSince orders RFC 3339 times oldest first, an unknown one last.
func compareSince(a, b string) int {
	at, aErr := time.Parse(time.RFC3339Nano, a)
	bt, bErr := time.Parse(time.RFC3339Nano, b)
	switch {
	case aErr != nil && bErr != nil:
		return 0
	case aErr != nil:
		return 1
	case bErr != nil:
		return -1
	}
	return at.Compare(bt)
}

func (s *Server) attentionRun(ctx context.Context, run *domain.Run) (*attentionRun, error) {
	r := &attentionRun{run: run}
	if !run.Status.Terminal() {
		r.paused = s.cfg.Runs.Paused(run.ID)
		r.inputs = s.cfg.Runs.PendingInputs(run.ID)
		if run.ACP && len(r.inputs) > 0 {
			r.pending = s.cfg.Runs.ACPPending(run.ID)
		}
	}
	if s.cfg.Control != nil {
		if holder, ok := s.cfg.Control.Status(string(run.ID)); ok {
			r.controller = holder.MemberID
		}
	}
	if run.UnansweredQuestions > 0 || r.controller != "" {
		open, err := s.cfg.Store.ListOpenRoomRequests(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		r.open = open
	}
	return r, nil
}

// memberNames is memberName in web/src/lib/member-name.ts: a merged-away
// id names the member it became, marked merged; an unknown id names itself.
func (s *Server) memberNames(ctx context.Context) (func(domain.MemberID) string, error) {
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return nil, err
	}
	display := make(map[domain.MemberID]string, len(members))
	for _, m := range members {
		display[m.ID] = m.DisplayName
	}
	mergedInto := map[domain.MemberID]domain.MemberID{}
	if ms, ok := s.cfg.Store.(store.MergeStore); ok {
		merges, err := ms.ListMerges(ctx, "")
		if err != nil {
			return nil, err
		}
		for _, m := range merges {
			mergedInto[m.AbsorbedID] = m.SurvivorID
		}
	}
	return func(id domain.MemberID) string {
		current := id
		for hops := 0; mergedInto[current] != "" && hops < 16; hops++ {
			current = mergedInto[current]
		}
		name, ok := display[current]
		switch {
		case !ok:
			return string(id)
		case current != id:
			return name + " (merged)"
		}
		return name
	}, nil
}

// attentionRow is one entry of needsYouConditions.
type attentionRow struct {
	kind  string
	holds func(r *attentionRun) bool
	// resolvers are the members the row waits on.
	resolvers  func(r *attentionRun) []domain.MemberID
	admins     bool
	background bool
	completed  bool
	fill       func(r *attentionRun, viewer attentionViewer, item *protocol.AttentionItem)
}

const (
	usageLimitPrefix = "Usage limit reached on "
	needsInputPrefix = "needs input: "
)

var questionKinds = []string{"question", "form", "extension_ui"}

func (r *attentionRun) live() bool {
	return r.run.Status.Live()
}

// firstInput is the agent's own oldest request of one of kinds.
func (r *attentionRun) firstInput(kinds ...string) *domain.RunInputRequest {
	for i := range r.inputs {
		if r.inputs[i].SessionID != domain.SudoSessionID && slices.Contains(kinds, r.inputs[i].Kind) {
			return &r.inputs[i]
		}
	}
	return nil
}

func (r *attentionRun) sudoInput() *domain.RunInputRequest {
	for i := range r.inputs {
		if r.inputs[i].SessionID == domain.SudoSessionID {
			return &r.inputs[i]
		}
	}
	return nil
}

func (r *attentionRun) openRoomQuestions() []*store.RoomMessage {
	var open []*store.RoomMessage
	for _, m := range r.open {
		if m.Kind == store.RoomMessageQuestion {
			open = append(open, m)
		}
	}
	return open
}

func (r *attentionRun) queuedMessages() []*store.RoomMessage {
	var queued []*store.RoomMessage
	for _, m := range r.open {
		if m.Kind == store.RoomMessageSteerRequest && m.ActorID != r.controller {
			queued = append(queued, m)
		}
	}
	return queued
}

func owner(r *attentionRun) []domain.MemberID { return []domain.MemberID{r.run.MemberID} }

func ownerOrController(r *attentionRun) []domain.MemberID {
	return []domain.MemberID{r.run.MemberID, r.controller}
}

func since(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (r *attentionRun) request(input *domain.RunInputRequest) *protocol.AttentionRequest {
	out := &protocol.AttentionRequest{ID: input.ID, Kind: input.Kind}
	if input.SessionID == domain.SudoSessionID {
		out.Title = input.Title
		out.Options = []protocol.AttentionOption{
			{ID: domain.SudoAllow, Label: "Allow once", Kind: "allow_once"},
			{ID: domain.SudoDeny, Label: "Deny", Kind: "reject_once"},
		}
		return out
	}
	for _, pending := range r.pending {
		if pending.ID != input.ID {
			continue
		}
		out.Title, out.Schema, out.URL = pending.Title, pending.Schema, pending.URL
		for _, o := range pending.Options {
			out.Options = append(out.Options, protocol.AttentionOption{ID: o.ID, Label: o.Name, Kind: o.Kind})
		}
	}
	return out
}

var attentionRows = []attentionRow{
	{
		kind:       protocol.AttentionKindPermission,
		background: true,
		admins:     true,
		holds:      func(r *attentionRun) bool { return r.sudoInput() != nil },
		resolvers:  owner,
		fill: func(r *attentionRun, _ attentionViewer, item *protocol.AttentionItem) {
			item.Reason = "Permission requested"
			item.Request = r.request(r.sudoInput())
		},
	},
	{
		kind:       protocol.AttentionKindPermission,
		background: true,
		holds:      func(r *attentionRun) bool { return r.firstInput("permission") != nil },
		resolvers:  ownerOrController,
		fill: func(r *attentionRun, _ attentionViewer, item *protocol.AttentionItem) {
			item.Reason = "Permission: answer in the terminal"
			if r.run.ACP {
				item.Reason = "Permission requested"
			}
			item.Request = r.request(r.firstInput("permission"))
		},
	},
	{
		kind:      protocol.AttentionKindQuestion,
		holds:     func(r *attentionRun) bool { return r.firstInput(questionKinds...) != nil },
		resolvers: ownerOrController,
		fill: func(r *attentionRun, _ attentionViewer, item *protocol.AttentionItem) {
			item.Reason = "Question: answer in the terminal"
			if r.run.ACP {
				item.Reason = "Question from the agent"
			}
			item.Request = r.request(r.firstInput(questionKinds...))
		},
	},
	{
		kind:      protocol.AttentionKindQueuedMessage,
		holds:     func(r *attentionRun) bool { return r.controller != "" && len(r.queuedMessages()) > 0 },
		resolvers: func(r *attentionRun) []domain.MemberID { return []domain.MemberID{r.controller} },
		fill: func(r *attentionRun, viewer attentionViewer, item *protocol.AttentionItem) {
			m := r.queuedMessages()[0]
			sender := m.ActorDisplayName
			if sender == "" {
				sender = viewer.name(m.ActorID)
			}
			item.Reason, item.Since = sender+" sent a message, approve to deliver", since(&m.CreatedAt)
			item.Message = &protocol.AttentionMessage{ID: m.ID, Body: m.Body, From: string(m.ActorID)}
		},
	},
	{
		kind:      protocol.AttentionKindRoomQuestion,
		holds:     func(r *attentionRun) bool { return r.run.UnansweredQuestions > 0 },
		resolvers: owner,
		fill: func(r *attentionRun, viewer attentionViewer, item *protocol.AttentionItem) {
			open := r.openRoomQuestions()
			if len(open) == 0 {
				item.Reason = "A teammate asked a question"
				return
			}
			q := open[0]
			asker := q.ActorDisplayName
			if asker == "" {
				asker = viewer.name(q.ActorID)
			}
			item.Reason, item.Since = asker+" asked you: "+q.Body, since(&q.CreatedAt)
			item.Message = &protocol.AttentionMessage{ID: q.ID, Body: q.Body, From: string(q.ActorID)}
		},
	},
	{
		kind:       protocol.AttentionKindDisconnected,
		background: true,
		holds:      func(r *attentionRun) bool { return r.run.Activity == domain.ActivityDisconnected },
		resolvers:  owner,
		fill: func(r *attentionRun, _ attentionViewer, item *protocol.AttentionItem) {
			item.Reason = "Agent disconnected"
			if r.run.Reason != "" {
				item.Reason = plainReason(r.run.Reason)
			}
		},
	},
	{
		kind:       protocol.AttentionKindBlocked,
		background: true,
		holds: func(r *attentionRun) bool {
			return r.run.Activity == domain.ActivityNeedsYou && strings.HasPrefix(r.run.Reason, domain.BlockedReasonPrefix)
		},
		resolvers: owner,
		fill: func(r *attentionRun, _ attentionViewer, item *protocol.AttentionItem) {
			item.Reason = "Blocked: " + strings.TrimPrefix(r.run.Reason, domain.BlockedReasonPrefix)
		},
	},
	{
		kind:       protocol.AttentionKindNeedsInput,
		background: true,
		holds:      func(r *attentionRun) bool { return r.run.Activity == domain.ActivityNeedsYou },
		resolvers:  owner,
		fill: func(r *attentionRun, _ attentionViewer, item *protocol.AttentionItem) {
			reason := r.run.Reason
			switch {
			case turnCutShort(reason):
				item.Reason = plainReason(reason) + " · reply to continue"
			case strings.HasPrefix(reason, waitBrokenPrefix):
				item.Reason = "Wait broken: " + strings.TrimPrefix(reason, waitBrokenPrefix) + " · reply to continue"
			case strings.HasPrefix(reason, needsInputPrefix), strings.HasPrefix(reason, usageLimitPrefix):
				item.Reason = plainReason(reason)
			default:
				item.Reason = "Open the session to see its request"
			}
		},
	},
	{
		kind:       protocol.AttentionKindReady,
		background: true,
		completed:  true,
		holds: func(r *attentionRun) bool {
			return r.run.Review == domain.ReviewReady &&
				(r.run.Status == domain.RunCompleted || r.live() && r.run.Activity == domain.ActivityFinished)
		},
		resolvers: owner,
		fill: func(r *attentionRun, _ attentionViewer, item *protocol.AttentionItem) {
			item.Reason = "Your agent reports this task is finished"
			if at := since(r.run.ReviewChangedAt); at != "" {
				item.Since = at
			}
		},
	},
	{
		kind:       protocol.AttentionKindFailed,
		background: true,
		holds:      func(r *attentionRun) bool { return r.run.Activity == domain.ActivityFailed && r.run.OutcomeUnseen },
		resolvers:  owner,
		fill: func(r *attentionRun, _ attentionViewer, item *protocol.AttentionItem) {
			item.Reason = "Agent reported failure; review the result. Session remains open for follow-up."
			if r.run.Reason != "" && r.run.Reason != "agent reported failure" {
				item.Reason = plainReason(r.run.Reason)
			}
		},
	},
}

func (row attentionRow) applies(r *attentionRun, viewer attentionViewer) bool {
	run := r.run
	standing := r.live() && !r.paused || row.completed && run.Status == domain.RunCompleted
	if !standing || !row.background && run.Mode == domain.LaunchHeadless || !row.holds(r) {
		return false
	}
	return row.admins && viewer.admin || slices.Contains(row.resolvers(r), viewer.id)
}

func (row attentionRow) item(r *attentionRun, viewer attentionViewer) protocol.AttentionItem {
	run := r.run
	item := protocol.AttentionItem{
		Kind: row.kind, RunID: string(run.ID), WorkspaceID: string(run.WorkspaceID),
		Title: push.RunTitle(run), Harness: run.Harness, Since: since(run.StatusChangedAt),
	}
	row.fill(r, viewer, &item)
	return item
}

func attentionItem(r *attentionRun, viewer attentionViewer) (protocol.AttentionItem, bool) {
	for _, row := range attentionRows {
		if row.applies(r, viewer) {
			return row.item(r, viewer), true
		}
	}
	return protocol.AttentionItem{}, false
}
