package tools

import (
	"cmp"
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type summarizer func(s *Server, ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error

// actSummaries are the cards of the lifecycle, swarm, delivery, pull request
// and memory methods, each naming what it acts on.
var actSummaries = map[string]summarizer{
	protocol.MethodRunRelaunch: runSummary(func(protocol.RunIDParams) string { return "Relaunch %s" }),
	protocol.MethodRunModeSwitch: runSummary(func(p protocol.RunModeSwitchParams) string {
		if p.Mode == "acp" {
			return "Switch %s to Enhanced mode"
		}
		return "Switch %s to Standard mode"
	}),
	protocol.MethodRunProtect: runSummary(func(p protocol.RunProtectParams) string {
		if p.Protected {
			return "Protect %s from cleanup"
		}
		return "Stop protecting %s from cleanup"
	}),
	protocol.MethodRunArchive: runSummary(func(p protocol.RunArchiveParams) string {
		if p.Archived {
			return "Archive %s"
		}
		return "Restore %s from the archive"
	}),
	protocol.MethodMissionWorkerRelease: runSummary(func(protocol.MissionWorkerReleaseParams) string {
		return "Release your control of worker %s"
	}),
	protocol.MethodRunHandoff:               (*Server).summarizeHandoff,
	protocol.MethodMissionArchive:           swarmSummary("Archive"),
	protocol.MethodMissionUnarchive:         swarmSummary("Unarchive"),
	protocol.MethodMissionReplaceIntegrator: (*Server).summarizeReplaceIntegrator,
	protocol.MethodIntegrationDecide:        (*Server).summarizeDelivery,
	protocol.MethodIntegrationDeliver:       (*Server).summarizeDelivery,
	protocol.MethodRunPRCreate:              (*Server).summarizePRCreate,
	protocol.MethodRunPRMerge:               (*Server).summarizePRMerge,
	protocol.MethodMemoryAdd:                (*Server).summarizeMemoryAdd,
	protocol.MethodMemoryPin:                noteSummary("Pin for every new run's brief"),
	protocol.MethodMemoryUnpin:              noteSummary("Unpin"),
}

type runParams interface {
	protocol.RunIDParams | protocol.RunModeSwitchParams | protocol.RunProtectParams | protocol.RunArchiveParams | protocol.MissionWorkerReleaseParams
}

// runSummary is the card of a method on one run: verb is what it does, with
// %s where the run goes.
func runSummary[P runParams](verb func(P) string) summarizer {
	return func(s *Server, ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
		p, err := protocol.DecodeParams[P](req.Params)
		if err != nil {
			return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		id, _ := protocol.DecodeParams[protocol.RunIDParams](req.Params)
		if id.RunID == "" {
			return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
		}
		run, rpcErr := s.ownedRun(ctx, id.RunID, c)
		if rpcErr != nil {
			return rpcErr
		}
		c.Summary = fmt.Sprintf(verb(p), fmt.Sprintf("run %s (%q)", id.RunID, runLabel(run))) + ownedBy(c)
		return nil
	}
}

func (s *Server) summarizeHandoff(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.RunHandoffParams](req.Params)
	if err != nil || p.RunID == "" || p.ToMemberID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id and to_member_id are required"}
	}
	run, rpcErr := s.ownedRun(ctx, p.RunID, c)
	if rpcErr != nil {
		return rpcErr
	}
	to, rpcErr := s.memberName(ctx, p.ToMemberID)
	if rpcErr != nil {
		return rpcErr
	}
	c.Summary = fmt.Sprintf("Hand run %s (%q)%s to %s, who then owns it", p.RunID, runLabel(run), commaAfter(ownedBy(c)), to)
	return nil
}

// memberName is member's display name, else their id.
func (s *Server) memberName(ctx context.Context, member string) (string, *protocol.Error) {
	var members protocol.MemberListResult
	if rpcErr := s.call(ctx, protocol.MethodMemberList, nil, &members); rpcErr != nil {
		return "", rpcErr
	}
	for _, m := range members.Members {
		if m.ID == member && m.DisplayName != "" {
			return truncate(m.DisplayName), nil
		}
	}
	return member, nil
}

// swarm is the swarm id names, with c marked as acting on another
// member's swarm when it is one.
func (s *Server) swarm(ctx context.Context, id string, c *protocol.AssistantConfirmation) (protocol.Mission, *protocol.Error) {
	var shown protocol.MissionShowResult
	if rpcErr := s.call(ctx, protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: id}, &shown); rpcErr != nil {
		return protocol.Mission{}, rpcErr
	}
	return shown.Mission, s.owner(ctx, shown.Mission.AccountableHumanID, c)
}

func swarmSummary(verb string) summarizer {
	return func(s *Server, ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
		p, err := protocol.DecodeParams[protocol.MissionIDParams](req.Params)
		if err != nil || p.MissionID == "" {
			return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": mission_id is required"}
		}
		m, rpcErr := s.swarm(ctx, p.MissionID, c)
		if rpcErr != nil {
			return rpcErr
		}
		c.Summary = fmt.Sprintf("%s swarm %s (%q)%s", verb, m.ID, truncate(m.Objective), ownedBy(c))
		return nil
	}
}

func (s *Server) summarizeReplaceIntegrator(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.MissionReplaceIntegratorParams](req.Params)
	if err != nil || p.MissionID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": mission_id is required"}
	}
	m, rpcErr := s.swarm(ctx, p.MissionID, c)
	if rpcErr != nil {
		return rpcErr
	}
	account, rpcErr := s.memberName(ctx, p.Integrator.AccountMemberID)
	if rpcErr != nil {
		return rpcErr
	}
	agent := p.Integrator.Harness
	if p.Integrator.Model != "" {
		agent += " " + p.Integrator.Model
	}
	c.Summary = fmt.Sprintf("Replace integrator run %s of swarm %s (%q)%s with %s (%s) on %s's account", cmp.Or(m.CurrentIntegratorRunID, "(none)"),
		m.ID, truncate(m.Objective), commaAfter(ownedBy(c)), truncate(agent), p.Integrator.Mode, account)
	return nil
}

func (s *Server) summarizeDelivery(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.IntegrationDecideParams](req.Params)
	if err != nil || p.WorkspaceID == "" || p.CandidateID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": workspace_id and candidate_id are required"}
	}
	var shown protocol.IntegrationShowResult
	if rpcErr := s.call(ctx, protocol.MethodIntegrationShow, protocol.IntegrationShowParams{WorkspaceID: p.WorkspaceID, CandidateID: p.CandidateID}, &shown); rpcErr != nil {
		return rpcErr
	}
	verb := "Deliver"
	if req.Method == protocol.MethodIntegrationDecide {
		verb = "Approve delivering"
		if !p.Approve {
			verb = "Reject delivering"
		}
	}
	cd := shown.Candidate
	c.Summary = fmt.Sprintf("%s candidate %s to %s", verb, cd.CandidateID, truncate(cd.TargetRef))
	if cd.MissionID == "" {
		return nil
	}
	m, rpcErr := s.swarm(ctx, cd.MissionID, c)
	if rpcErr != nil {
		return rpcErr
	}
	c.Summary += fmt.Sprintf(" for swarm %s (%q)%s", m.ID, truncate(m.Objective), ownedBy(c))
	return nil
}

func (s *Server) summarizePRCreate(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.RunPRCreateParams](req.Params)
	if err != nil || p.RunID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
	}
	run, rpcErr := s.ownedRun(ctx, p.RunID, c)
	if rpcErr != nil {
		return rpcErr
	}
	kind := "a pull request"
	if p.Draft {
		kind = "a draft pull request"
	}
	c.Summary = fmt.Sprintf("Open %s from run %s (%q)%s, branch %s at %.7s: %q", kind, p.RunID, runLabel(run), ownedBy(c), truncate(p.Expected.Branch), p.Expected.Head, truncate(p.Title))
	return nil
}

func (s *Server) summarizePRMerge(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.RunPRMergeParams](req.Params)
	if err != nil || p.RunID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
	}
	run, rpcErr := s.ownedRun(ctx, p.RunID, c)
	if rpcErr != nil {
		return rpcErr
	}
	pr := "its pull request"
	if p.Repository != "" {
		pr = fmt.Sprintf("%s#%d", truncate(p.Repository), p.Number)
	}
	c.Summary = fmt.Sprintf("Merge (%s) %s of run %s (%q)%s at %.7s into %s", cmp.Or(p.Method, "default"), pr, p.RunID, runLabel(run), commaAfter(ownedBy(c)),
		p.ExpectedHead, cmp.Or(truncate(p.ExpectedBase), "its base"))
	return nil
}

func noteSummary(verb string) summarizer {
	return func(s *Server, ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
		p, err := protocol.DecodeParams[protocol.MemoryPinParams](req.Params)
		if err != nil || p.WorkspaceID == "" || p.ID == "" {
			return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": workspace_id and id are required"}
		}
		var shown protocol.MemoryShowResult
		if rpcErr := s.call(ctx, protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: p.WorkspaceID, ID: p.ID}, &shown); rpcErr != nil {
			return rpcErr
		}
		c.Summary = fmt.Sprintf("%s note %s (%s): %q", verb, p.ID, shown.Item.Kind, truncate(cmp.Or(shown.Item.Title, shown.Item.Body)))
		return nil
	}
}

func (s *Server) summarizeMemoryAdd(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.MemoryAddParams](req.Params)
	if err != nil || p.WorkspaceID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": workspace_id is required"}
	}
	var ws protocol.WorkspaceGetResult
	if rpcErr := s.call(ctx, protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: p.WorkspaceID}, &ws); rpcErr != nil {
		return rpcErr
	}
	c.Summary = fmt.Sprintf("Remember a %s for everyone in %s: %q", p.Kind, ws.Workspace.Name, truncate(p.Text))
	return nil
}

// commaAfter closes an ownedBy clause before the sentence goes on.
func commaAfter(clause string) string {
	if clause == "" {
		return ""
	}
	return clause + ","
}
