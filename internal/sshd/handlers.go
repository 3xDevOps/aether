package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

func (s *Server) serverInfo(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.ServerInfoResult{
		ServerVersion:       version.Version,
		ProtocolVersion:     protocol.Version,
		Time:                time.Now().UTC().Format(time.RFC3339),
		Member:              protocol.MemberFromDomain(m),
		TailnetHostname:     s.cfg.TailnetHostname,
		TailnetIdentityAuth: s.cfg.WhoIs != nil,
	}, nil
}

func (s *Server) workspaceList(ctx context.Context, _ domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	list, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	out := make([]protocol.Workspace, 0, len(list))
	for _, w := range list {
		out = append(out, protocol.WorkspaceFromDomain(w))
	}
	return protocol.WorkspaceListResult{Workspaces: out}, nil
}

func (s *Server) workspaceGet(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceGetParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(p.WorkspaceID))
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.WorkspaceGetResult{Workspace: protocol.WorkspaceFromDomain(ws)}, nil
}

func (s *Server) memberList(ctx context.Context, _ domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	list, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	out := make([]protocol.Member, 0, len(list))
	for _, m := range list {
		out = append(out, protocol.MemberFromDomain(m))
	}
	return protocol.MemberListResult{Members: out}, nil
}

func (s *Server) memberApprove(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.MemberApproveParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.MemberID == "" {
		return nil, invalidParams("member_id is required")
	}
	caller, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	if caller.Role != domain.RoleAdmin {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "member.approve requires the admin role"}
	}
	id := domain.MemberID(p.MemberID)
	if approveErr := s.cfg.Store.ApproveMember(ctx, id); approveErr != nil {
		return nil, rpcError(approveErr)
	}
	approved, err := s.cfg.Store.GetMember(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.MemberApproveResult{Member: protocol.MemberFromDomain(approved)}, nil
}

func (s *Server) runLaunch(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunLaunchParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.Harness == "" {
		return nil, invalidParams("workspace_id and harness are required")
	}
	mode := domain.LaunchMode(p.Mode)
	if p.Mode == "" {
		mode = domain.LaunchTUI
	}
	if !mode.Valid() {
		return nil, invalidParams("invalid mode: " + p.Mode)
	}
	// A taskless launch drops the member into the agent's interactive TUI with
	// no seeded prompt. Headless has no interactive surface, so it still needs
	// a task to have anything to do.
	if p.Task == "" && mode == domain.LaunchHeadless {
		return nil, invalidParams("task is required in headless mode")
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	admission, err := AuthorizeLaunch(ctx, s.cfg.Store, member, p.AccountMemberID)
	if err != nil {
		if errors.Is(err, permissions.ErrDenied) {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunLaunch + ": " + err.Error()}
		}
		return nil, rpcError(err)
	}
	account := admission.Account.ID
	run, err := launchWithOptions(ctx, s.cfg.Runs, domain.WorkspaceID(p.WorkspaceID), member, account, p.Task, p.Harness, mode, domain.LaunchOptions{CachedBase: p.CachedBase})
	if err != nil {
		if errors.Is(err, errLaunchOptionsUnsupported) {
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run.launch: cached base retry is not supported by this server"}
		}
		return nil, rpcError(err)
	}
	return protocol.RunResult{Run: protocol.RunFromDomain(run)}, nil
}

func (s *Server) runList(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunListParams](params)
	if perr != nil {
		return nil, perr
	}
	var (
		runs []*domain.Run
		err  error
	)
	switch {
	case p.WorkspaceID != "":
		runs, err = s.cfg.Store.ListRunsByWorkspace(ctx, domain.WorkspaceID(p.WorkspaceID))
	case p.MemberID != "":
		runs, err = s.cfg.Store.ListRunsByMember(ctx, domain.MemberID(p.MemberID))
	case p.ActiveOnly:
		runs, err = s.cfg.Store.ListActiveRuns(ctx)
	default:
		var workspaces []*domain.Workspace
		workspaces, err = s.cfg.Store.ListWorkspaces(ctx)
		for _, ws := range workspaces {
			if err != nil {
				break
			}
			var wr []*domain.Run
			wr, err = s.cfg.Store.ListRunsByWorkspace(ctx, ws.ID)
			runs = append(runs, wr...)
		}
	}
	if err != nil {
		return nil, rpcError(err)
	}
	out := make([]protocol.Run, 0, len(runs))
	for _, r := range runs {
		if p.MemberID != "" && r.MemberID != domain.MemberID(p.MemberID) {
			continue
		}
		if p.ActiveOnly && r.Status.Terminal() {
			continue
		}
		wr := protocol.RunFromDomain(r)
		if s.cfg.Runs != nil {
			wr.Paused = s.cfg.Runs.Paused(r.ID)
		}
		out = append(out, wr)
	}
	return protocol.RunListResult{Runs: out}, nil
}

func (s *Server) runGet(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := runIDParams(params)
	if perr != nil {
		return nil, perr
	}
	run, err := s.cfg.Store.GetRun(ctx, p)
	if err != nil {
		return nil, rpcError(err)
	}
	wr := protocol.RunFromDomain(run)
	if s.cfg.Runs != nil {
		wr.Paused = s.cfg.Runs.Paused(run.ID)
	}
	return protocol.RunResult{Run: wr}, nil
}

func runIDParams(params json.RawMessage) (domain.RunID, *protocol.Error) {
	p, perr := decodeParams[protocol.RunIDParams](params)
	if perr != nil {
		return "", perr
	}
	if p.RunID == "" {
		return "", invalidParams("run_id is required")
	}
	return domain.RunID(p.RunID), nil
}

func (s *Server) runKill(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return s.runAct(ctx, member, params, s.cfg.Runs.Kill)
}

func (s *Server) runDelete(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return s.runAct(ctx, member, params, s.cfg.Runs.DeleteRun)
}

// runArchive hides a finished run from the board, or restores it. The
// guard above has already checked Kill against the run, matching
// run.delete's gate.
func (s *Server) runArchive(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunArchiveParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" {
		return nil, invalidParams("run_id is required")
	}
	run, err := s.cfg.Runs.SetArchived(ctx, domain.RunID(p.RunID), member, p.Archived)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.RunResult{Run: protocol.RunFromDomain(run)}, nil
}

func (s *Server) runPause(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return s.runAct(ctx, member, params, s.cfg.Runs.Pause)
}

func (s *Server) runResume(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return s.runAct(ctx, member, params, s.cfg.Runs.Resume)
}

func (s *Server) runAct(ctx context.Context, member domain.MemberID, params json.RawMessage, act func(context.Context, domain.RunID, domain.MemberID) error) (any, *protocol.Error) {
	id, perr := runIDParams(params)
	if perr != nil {
		return nil, perr
	}
	if err := act(ctx, id, member); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}

func (s *Server) runInject(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	rooms, perr := s.rooms()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.RunInjectParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" || p.Message == "" || p.IdempotencyKey == "" {
		return nil, invalidParams("run_id, message, and idempotency_key are required")
	}
	if len(p.IdempotencyKey) > protocol.CollaborationMaxIdempotencyKeyBytes {
		return nil, invalidParams("idempotency_key is too long")
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	result, err := rooms.Post(ctx, collab.MessageInput{
		WorkspaceID:    run.WorkspaceID,
		RunID:          run.ID,
		ActorID:        member,
		Kind:           store.RoomMessageSteerRequest,
		Body:           p.Message,
		IdempotencyKey: p.IdempotencyKey,
	})
	if err != nil {
		return nil, collaborationRPCError(err)
	}
	return roomMutationResult(result), nil
}

func (s *Server) runClose(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunCloseParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" {
		return nil, invalidParams("run_id is required")
	}
	outcome := domain.RunStatus(p.Outcome)
	if outcome != domain.RunMerged && outcome != domain.RunAbandoned {
		return nil, invalidParams(`outcome must be "merged" or "abandoned"`)
	}
	id := domain.RunID(p.RunID)
	if err := s.cfg.Runs.CloseRun(ctx, id, member, outcome); err != nil {
		return nil, rpcError(err)
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.RunResult{Run: protocol.RunFromDomain(run)}, nil
}

// runRelaunch re-enters the addressed retained TUI run. The retained
// container still carries the account environment it was launched with, so
// account-share authorization must be current before the scheduler attaches.
func (s *Server) runRelaunch(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	id, perr := runIDParams(params)
	if perr != nil {
		return nil, perr
	}

	// The generic guard above is defense in depth. Re-resolve every
	// authorization fact while holding authorizationMu so a revocation
	// cannot commit between this check and retained-run admission.
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	target, err := resolveRunTarget(ctx, s.cfg.Store, id)
	if err != nil {
		return nil, rpcError(err)
	}
	if cerr := permissions.Check(permissions.Steer, actor, target); cerr != nil {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunRelaunch + ": " + cerr.Error()}
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	if _, perr := s.launchAccount(ctx, member, string(run.AccountMember())); perr != nil {
		return nil, perr
	}
	reopened, err := s.cfg.Runs.Relaunch(ctx, id, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.RunResult{Run: protocol.RunFromDomain(reopened)}, nil
}

func (s *Server) runHandoff(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunHandoffParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" || p.ToMemberID == "" {
		return nil, invalidParams("run_id and to_member_id are required")
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	target, err := resolveRunTarget(ctx, s.cfg.Store, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	if cerr := permissions.Check(permissions.Handoff, actor, target); cerr != nil {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunHandoff + ": " + cerr.Error()}
	}

	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	// A run must land on someone who can act on it. Handing one to a
	// viewer (or a member awaiting approval) orphans it: nobody but an
	// admin could then steer or kill it. Launch is the capability that
	// separates the roles that may own a run from the ones that may not.
	to := domain.MemberID(p.ToMemberID)
	recipient, err := s.cfg.Store.GetMember(ctx, to)
	if err != nil {
		return nil, rpcError(err)
	}
	if recipient.Pending {
		return nil, invalidParams(fmt.Sprintf("cannot hand off to %s: membership is pending admin approval", recipient.DisplayName))
	}
	recipientActor := permissions.Actor{ID: recipient.ID, Role: recipient.Role}
	if derr := permissions.Check(permissions.Launch, recipientActor, permissions.Target{}); derr != nil {
		return nil, invalidParams(fmt.Sprintf("cannot hand off to %s: viewers cannot own runs", recipient.DisplayName))
	}
	from := run.MemberID
	outbox, ok := s.cfg.Store.(store.HandoffOutboxStore)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run.handoff: durable handoff outbox is not configured"}
	}
	operationID := fmt.Sprintf("handoff-%s-%d-%d", run.ID, time.Now().UTC().UnixNano(), s.handoffSeq.Add(1))
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	transfer := func() error {
		return outbox.TransferRunWithHandoff(commitCtx, &store.HandoffOutbox{
			ID: operationID, WorkspaceID: run.WorkspaceID, RunID: run.ID,
			ActorID: member, FromMemberID: from, ToMemberID: to,
			EvidenceState:    store.HandoffEvidencePending,
			PublicationState: store.HandoffPublicationPending,
		})
	}
	if s.cfg.Control != nil {
		displaced, transferErr := s.cfg.Control.AdmitRevoke(p.RunID, transfer)
		if transferErr != nil {
			return nil, rpcError(transferErr)
		}
		if displaced != nil {
			s.cancelControlAttach(p.RunID, displaced.SessionID, displaced.Generation, errAttachControlRevoked)
		}
	} else if err := transfer(); err != nil {
		return nil, rpcError(err)
	}
	// The ownership commit is the handoff's durable linearization point.
	// Every post-transfer side effect is an outbox phase and may safely be
	// retried after this handler or the whole server exits.
	s.recordHandoffContext(commitCtx, operationID, run, member, from, to, outbox)
	return struct{}{}, nil
}

type handoffOperationRecorder interface {
	RecordHandoffOperation(context.Context, domain.RunID, domain.MemberID, string, time.Time) error
}

const handoffReplayInterval = 5 * time.Second

func (s *Server) recordHandoffContext(ctx context.Context, operationID string, run *domain.Run, actor, from, to domain.MemberID, outbox store.HandoffOutboxStore) {
	if run == nil {
		return
	}
	h, err := outbox.GetHandoffOutbox(ctx, operationID)
	if err != nil {
		slog.Warn("sshd: load handoff outbox after transfer", "operation", operationID, "error", err)
		return
	}
	if h.WorkspaceID == "" {
		h.WorkspaceID = run.WorkspaceID
	}
	if h.RunID == "" {
		h.RunID = run.ID
	}
	if h.ActorID == "" {
		h.ActorID = actor
	}
	if h.FromMemberID == "" {
		h.FromMemberID = from
	}
	if h.ToMemberID == "" {
		h.ToMemberID = to
	}
	s.processHandoffOutbox(ctx, h, outbox, s.cfg.Services.Rooms, handoffEvidenceCapture(s.cfg.Services.Evidence))
}

func handoffEvidenceCapture(v EvidenceService) EvidenceCaptureService {
	capture, _ := v.(EvidenceCaptureService)
	return capture
}

func (s *Server) replayHandoffOutbox(ctx context.Context, outbox store.HandoffOutboxStore, rooms RoomService, evidenceCapture EvidenceCaptureService) {
	ticker := time.NewTicker(handoffReplayInterval)
	defer ticker.Stop()
	for {
		s.replayPendingHandoffs(ctx, outbox, rooms, evidenceCapture)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) replayPendingHandoffs(ctx context.Context, outbox store.HandoffOutboxStore, rooms RoomService, evidenceCapture EvidenceCaptureService) {
	if pager, ok := outbox.(store.HandoffOutboxPager); ok {
		before := ""
		for {
			pending, next, err := pager.ListPendingHandoffOutboxPage(ctx, before, store.MaxCollaborationPageSize)
			if err != nil {
				slog.Warn("sshd: list pending handoffs", "error", err)
				return
			}
			for _, h := range pending {
				if h == nil {
					continue
				}
				s.processHandoffOutbox(ctx, h, outbox, rooms, evidenceCapture)
			}
			if next == "" || next == before {
				return
			}
			before = next
		}
	}
	pending, err := outbox.ListPendingHandoffOutbox(ctx, store.MaxCollaborationPageSize)
	if err != nil {
		slog.Warn("sshd: list pending handoffs", "error", err)
		return
	}
	for _, h := range pending {
		if h == nil {
			continue
		}
		s.processHandoffOutbox(ctx, h, outbox, rooms, evidenceCapture)
	}
}

func (s *Server) processHandoffOutbox(ctx context.Context, h *store.HandoffOutbox, outbox store.HandoffOutboxStore, rooms RoomService, evidenceCapture EvidenceCaptureService) {
	if h == nil {
		return
	}
	if h.TimelineState == store.HandoffTimelinePending {
		if _, err := s.cfg.Bus.Publish(ctx, events.Event{
			ID: "handoff:" + h.ID + ":timeline", Time: h.CreatedAt,
			WorkspaceID: h.WorkspaceID, RunID: h.RunID, ActorID: h.ActorID,
			Payload: events.TimelinePayload{Kind: events.TimelineHandoff, Message: string(h.ToMemberID)},
		}); err != nil {
			slog.Warn("sshd: publish handoff timeline", "operation", h.ID, "error", err)
			return
		}
		if err := outbox.MarkHandoffTimeline(ctx, h.ID); err != nil {
			slog.Warn("sshd: mark handoff timeline", "operation", h.ID, "error", err)
			return
		}
		h.TimelineState = store.HandoffTimelinePublished
	}
	if h.CoauthorState == store.HandoffCoauthorPending {
		if recorder, ok := s.cfg.Runs.(handoffOperationRecorder); ok {
			if err := recorder.RecordHandoffOperation(ctx, h.RunID, h.FromMemberID, h.ID, h.CreatedAt); err != nil {
				slog.Warn("sshd: apply handoff co-author", "operation", h.ID, "error", err)
				return
			}
		} else {
			s.cfg.Runs.RecordHandoff(ctx, h.RunID, h.FromMemberID)
		}
		if err := outbox.MarkHandoffCoauthor(ctx, h.ID); err != nil {
			slog.Warn("sshd: mark handoff co-author", "operation", h.ID, "error", err)
			return
		}
		h.CoauthorState = store.HandoffCoauthorPublished
	}
	if h.EvidenceState == store.HandoffEvidencePending {
		state, packetID, complete := captureHandoffEvidence(ctx, h, evidenceCapture)
		if !complete {
			return
		}
		if err := outbox.SetHandoffEvidence(ctx, h.ID, packetID, state); err != nil {
			slog.Warn("sshd: persist handoff evidence state", "operation", h.ID, "error", err)
			return
		}
		h.EvidenceState, h.EvidencePacketID = state, packetID
	}
	if h.PublicationState == store.HandoffPublicationPending {
		if rooms == nil {
			slog.Warn("sshd: handoff room service unavailable", "operation", h.ID)
			return
		}
		evidenceContext := "evidence unavailable"
		if h.EvidenceState == store.HandoffEvidenceAvailable && h.EvidencePacketID != "" {
			evidenceContext = "evidence packet " + h.EvidencePacketID
		}
		body := fmt.Sprintf("handoff id=%s actor=%s outgoing=%s incoming=%s; %s",
			h.ID, h.ActorID, h.FromMemberID, h.ToMemberID, evidenceContext)
		if _, err := rooms.Post(ctx, collab.MessageInput{
			WorkspaceID: h.WorkspaceID, RunID: h.RunID, ActorID: h.ActorID,
			Kind: store.RoomMessageSystem, Body: body, IdempotencyKey: h.ID,
		}); err != nil {
			slog.Warn("sshd: publish handoff room record", "operation", h.ID, "error", err)
			return
		}
		if err := outbox.MarkHandoffPublished(ctx, h.ID, h.CreatedAt); err != nil {
			slog.Warn("sshd: mark handoff publication", "operation", h.ID, "error", err)
			return
		}
	}
}

func captureHandoffEvidence(ctx context.Context, h *store.HandoffOutbox, capture EvidenceCaptureService) (state, packetID string, complete bool) {
	if capture == nil {
		return store.HandoffEvidenceUnavailable, "", true
	}
	packet, err := capture.Capture(ctx, evidence.Request{
		RunID: h.RunID, CreatorID: h.ActorID, Trigger: store.EvidenceHandoff,
		IdempotencyKey: h.ID,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) ||
			errors.Is(err, gitengine.ErrCheckoutNotFound) ||
			errors.Is(err, gitengine.ErrRunMetadataNotFound) {
			return store.HandoffEvidenceUnavailable, "", true
		}
		// A transport, staging, or database error is operational. Keep the
		// pending state so the bounded replay worker can retry it.
		slog.Warn("sshd: handoff evidence capture pending", "operation", h.ID, "error", err)
		return "", "", false
	}
	if packet.ID == "" {
		slog.Warn("sshd: handoff evidence capture returned no packet", "operation", h.ID)
		return "", "", false
	}
	return store.HandoffEvidenceAvailable, packet.ID, true
}

func (s *Server) runPull(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	id, perr := runIDParams(params)
	if perr != nil {
		return nil, perr
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.RunPullResult{
		WorkspaceID: string(run.WorkspaceID),
		RepoPath:    "/" + string(run.WorkspaceID) + ".git",
		Branch:      run.Branch,
	}, nil
}
