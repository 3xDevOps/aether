package tools

import (
	"cmp"
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type summarizer func(s *Server, ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error

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
	protocol.MethodMissionArchive:           swarmSummary("Archive, closing its completed runs,"),
	protocol.MethodMissionUnarchive:         swarmSummary("Unarchive"),
	protocol.MethodMissionReplaceIntegrator: (*Server).summarizeReplaceIntegrator,
	protocol.MethodIntegrationDecide:        (*Server).summarizeDelivery,
	protocol.MethodIntegrationDeliver:       (*Server).summarizeDelivery,
	protocol.MethodRunPRCreate:              (*Server).summarizePRCreate,
	protocol.MethodRunPRMerge:               (*Server).summarizePRMerge,
	protocol.MethodMemoryAdd:                (*Server).summarizeMemoryAdd,
	protocol.MethodMemoryPin:                noteSummary("Pin, into new runs' briefs,"),
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
	c.Summary = fmt.Sprintf("Hand run %s (%q)%s to %s (%s), who then owns it", p.RunID, runLabel(run), commaAfter(ownedBy(c)), to, truncate(p.ToMemberID))
	return nil
}

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
	c.Summary = fmt.Sprintf("Replace integrator run %s of swarm %s (%q)%s with %s (%s) on %s's (%s) account", cmp.Or(m.CurrentIntegratorRunID, "(none)"),
		m.ID, truncate(m.Objective), commaAfter(ownedBy(c)), truncate(agent), p.Integrator.Mode, account, truncate(p.Integrator.AccountMemberID))
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
	effect := "moving " + truncate(cd.TargetRef)
	if r := cd.DeliveryRequest; r != nil && r.Action == protocol.DeliveryActionProposal {
		effect = "publishing a proposal ref for " + truncate(cd.TargetRef)
	}
	c.Summary = fmt.Sprintf("%s candidate %s, %s", verb, cd.CandidateID, effect)
	if cd.MissionID == "" {
		return nil
	}
	m, rpcErr := s.swarm(ctx, cd.MissionID, c)
	if rpcErr != nil {
		return rpcErr
	}
	c.Summary += fmt.Sprintf(", for swarm %s (%q)%s", m.ID, truncate(m.Objective), ownedBy(c))
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
	into := "the workspace's base branch"
	if p.Target.Repository != "" || p.Target.BaseBranch != "" {
		into = fmt.Sprintf("%s on %s", truncate(p.Target.BaseBranch), truncate(p.Target.Repository))
	}
	repo := ""
	if p.Repo != "" {
		repo = fmt.Sprintf(" (repo %s)", truncate(p.Repo))
	}
	c.Summary = fmt.Sprintf("Open %s from run %s (%q)%s%s, branch %s at %.7s, into %s: %q", kind, p.RunID, runLabel(run), ownedBy(c), repo,
		truncate(p.Expected.Branch), p.Expected.Head, into, truncate(p.Title))
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
		c.Summary = fmt.Sprintf("%s note %s (%s)%s: %q", verb, p.ID, shown.Item.Kind, forWhom(p.Scope), truncate(cmp.Or(shown.Item.Title, shown.Item.Body)))
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
	c.Summary = fmt.Sprintf("Remember a %s in %s%s: %q", p.Kind, ws.Workspace.Name, forWhom(p.Scope), truncate(p.Text))
	if p.Pin {
		c.Summary += ", pinned into new runs' briefs"
	}
	if p.Supersedes != "" {
		var old protocol.MemoryShowResult
		if rpcErr := s.call(ctx, protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: p.WorkspaceID, ID: p.Supersedes}, &old); rpcErr != nil {
			return rpcErr
		}
		c.Summary += fmt.Sprintf(", replacing note %s (%q)", truncate(p.Supersedes), truncate(cmp.Or(old.Item.Title, old.Item.Body)))
		if old.Item.PinnedAt != nil {
			c.Summary += " and moving its pin"
		}
	}
	return nil
}

func commaAfter(clause string) string {
	if clause == "" {
		return ""
	}
	return clause + ","
}

func forWhom(scope string) string {
	if scope == protocol.MemoryScopeMember {
		return " for you only"
	}
	return " for everyone"
}
