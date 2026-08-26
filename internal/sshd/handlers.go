package sshd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
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
	if p.WorkspaceID == "" || p.Task == "" || p.Harness == "" {
		return nil, invalidParams("workspace_id, task, and harness are required")
	}
	mode := domain.LaunchMode(p.Mode)
	if p.Mode == "" {
		mode = domain.LaunchTUI
	}
	if !mode.Valid() {
		return nil, invalidParams("invalid mode: " + p.Mode)
	}
	run, err := s.cfg.Runs.Launch(ctx, domain.WorkspaceID(p.WorkspaceID), member, p.Task, p.Harness, mode)
	if err != nil {
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
	p, perr := decodeParams[protocol.RunInjectParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" || p.Message == "" {
		return nil, invalidParams("run_id and message are required")
	}
	if err := s.cfg.Runs.Inject(ctx, domain.RunID(p.RunID), member, p.Message); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
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

func (s *Server) runRelaunch(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	id, perr := runIDParams(params)
	if perr != nil {
		return nil, perr
	}
	run, err := s.cfg.Runs.Relaunch(ctx, id, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.RunResult{Run: protocol.RunFromDomain(run)}, nil
}

func (s *Server) runHandoff(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunHandoffParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" || p.ToMemberID == "" {
		return nil, invalidParams("run_id and to_member_id are required")
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	if err := s.cfg.Store.TransferRun(ctx, run.ID, domain.MemberID(p.ToMemberID)); err != nil {
		return nil, rpcError(err)
	}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
		ActorID:     member,
		Payload:     events.TimelinePayload{Kind: events.TimelineHandoff, Message: p.ToMemberID},
	})
	return struct{}{}, nil
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
