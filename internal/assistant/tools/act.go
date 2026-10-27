package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type summarizer func(s *Server, ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error

var actSummaries = map[string]summarizer{
	protocol.MethodRunRelaunch: runSummary(func(protocol.RunIDParams) string { return "Relaunch %s" }),
	protocol.MethodRunClose: runSummary(func(p protocol.RunCloseParams) string {
		if p.Outcome == "merged" {
			return "Close %s as merged"
		}
		return "Close %s as abandoned"
	}),
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
	protocol.MethodMissionWorkerRelease:     (*Server).summarizeWorkerRelease,
	protocol.MethodRunHandoff:               (*Server).summarizeHandoff,
	protocol.MethodMissionArchive:           swarmSummary("Archive, closing its completed runs,"),
	protocol.MethodMissionUnarchive:         swarmSummary("Unarchive"),
	protocol.MethodMissionReplaceIntegrator: (*Server).summarizeReplaceIntegrator,
	protocol.MethodIntegrationDecide:        (*Server).summarizeDelivery,
	protocol.MethodIntegrationDeliver:       (*Server).summarizeDelivery,
	protocol.MethodRunPRCreate:              (*Server).summarizePRCreate,
	protocol.MethodRunPRMerge:               (*Server).summarizePRMerge,
	protocol.MethodMemoryAdd:                (*Server).summarizeMemoryAdd,
	protocol.MethodMemoryPin:                (*Server).summarizeMemoryPin,
	protocol.MethodMemoryUnpin:              (*Server).summarizeMemoryUnpin,
}

type runParams interface {
	protocol.RunIDParams | protocol.RunCloseParams | protocol.RunModeSwitchParams | protocol.RunProtectParams | protocol.RunArchiveParams
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

// summarizeWorkerRelease names the member whose control of the worker the
// release ends, which need not be the caller's.
func (s *Server) summarizeWorkerRelease(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.MissionWorkerReleaseParams](req.Params)
	if err != nil || p.RunID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
	}
	params, _ := json.Marshal(protocol.RunIDParams{RunID: p.RunID})
	run, rpcErr := s.getRun(ctx, params)
	if rpcErr != nil {
		return rpcErr
	}
	if run.MissionID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf("%s: run %s is not a swarm worker", req.Method, p.RunID)}
	}
	var shown protocol.MissionShowResult
	if rpcErr := s.call(ctx, protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: run.MissionID}, &shown); rpcErr != nil {
		return rpcErr
	}
	i := slices.IndexFunc(shown.Attempts, func(a protocol.Attempt) bool { return a.RunID == p.RunID })
	if i < 0 || !shown.Attempts[i].TakeoverActive {
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf("%s: nobody holds control of worker run %s; its integrator steers it", req.Method, p.RunID)}
	}
	worker := fmt.Sprintf("worker run %s (%q) of swarm %s (%q)", p.RunID, runLabel(run), shown.Mission.ID, truncate(shown.Mission.Objective))
	if holder := shown.Attempts[i].TakeoverMemberID; holder != s.Member {
		if rpcErr := s.owner(ctx, holder, c); rpcErr != nil {
			return rpcErr
		}
		c.Summary = fmt.Sprintf("Release control of %s, held by %s, so its integrator steers it again", worker, truncate(c.OtherOwner))
		return nil
	}
	c.Summary = fmt.Sprintf("Release your control of %s, so its integrator steers it again", worker)
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

// summarizePRMerge counts what GitHub reports could block the merge, so the
// member decides with the checks and reviews in view.
func (s *Server) summarizePRMerge(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.RunPRMergeParams](req.Params)
	if err != nil || p.RunID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
	}
	run, rpcErr := s.ownedRun(ctx, p.RunID, c)
	if rpcErr != nil {
		return rpcErr
	}
	var fb protocol.RunPRFeedbackResult
	if rpcErr := s.call(ctx, protocol.MethodRunPRFeedback, protocol.RunPRFeedbackParams{RunID: p.RunID, Repo: p.Repo, Limit: 30}, &fb); rpcErr != nil {
		return rpcErr
	}
	pr := "its pull request"
	if p.Repository != "" {
		pr = fmt.Sprintf("%s#%d", truncate(p.Repository), p.Number)
	}
	b := fb.Blockers()
	blockers := fmt.Sprintf("GitHub reports %d failing checks, %d pending checks and %d reviewers requesting changes", b.FailingChecks, b.PendingChecks, b.ChangesRequested)
	if fb.Error != "" {
		blockers = "GitHub's checks and reviews could not be read: " + truncate(fb.Error)
	}
	c.Summary = fmt.Sprintf("Merge (%s) %s of run %s (%q)%s at %.7s into %s; %s", cmp.Or(p.Method, "default"), pr, p.RunID, runLabel(run), commaAfter(ownedBy(c)),
		p.ExpectedHead, cmp.Or(truncate(p.ExpectedBase), "its base"), blockers)
	return nil
}

func (s *Server) note(ctx context.Context, req protocol.Request) (protocol.MemoryItem, *protocol.Error) {
	p, err := protocol.DecodeParams[protocol.MemoryShowParams](req.Params)
	if err != nil || p.WorkspaceID == "" || p.ID == "" {
		return protocol.MemoryItem{}, &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": workspace_id and id are required"}
	}
	var shown protocol.MemoryShowResult
	if rpcErr := s.call(ctx, protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: p.WorkspaceID, ID: p.ID}, &shown); rpcErr != nil {
		return protocol.MemoryItem{}, rpcErr
	}
	return shown.Item, nil
}

func (s *Server) summarizeMemoryPin(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	item, rpcErr := s.note(ctx, req)
	if rpcErr != nil {
		return rpcErr
	}
	p, _ := protocol.DecodeParams[protocol.MemoryPinParams](req.Params)
	c.Summary = fmt.Sprintf("Pin, into new runs' briefs, note %s (%s)%s: %q", item.ID, item.Kind, forWhom(cmp.Or(p.Scope, item.Scope)), truncate(cmp.Or(item.Title, item.Body)))
	return nil
}

// summarizeMemoryUnpin names whose briefs lose the note: a member pin is
// one member's, a workspace pin everyone's.
func (s *Server) summarizeMemoryUnpin(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	item, rpcErr := s.note(ctx, req)
	if rpcErr != nil {
		return rpcErr
	}
	pinned := ", which is not pinned"
	switch {
	case item.PinnedAt == nil:
	case item.Scope != protocol.MemoryScopeMember:
		pinned = fmt.Sprintf(", taking it out of everyone's briefs (pinned by %s)", truncate(cmp.Or(item.PinnedByName, item.PinnedBy)))
	case item.PinnedBy == s.Member:
		pinned = ", taking it out of your briefs"
	default:
		pinned = fmt.Sprintf(", taking it out of %s's briefs", truncate(cmp.Or(item.PinnedByName, item.PinnedBy)))
	}
	c.Summary = fmt.Sprintf("Unpin note %s (%s)%s: %q", item.ID, item.Kind, pinned, truncate(cmp.Or(item.Title, item.Body)))
	return nil
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
