// Package tools serves the assistant's member-scoped tool socket: the
// allowlisted subset of the control surface the assistant may call, graded
// into tiers, with approval gating on the destructive ones.
package tools

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/docs"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/templates"
)

const (
	maxRequestBytes = 64 << 10
	maxConns        = 16
	idleTimeout     = 5 * time.Minute
)

const (
	tierRead       = "read"
	tierReversible = "reversible"
	tierConfirmed  = "confirmed"
)

// tiers are the methods the assistant may call: every method of every op
// internal/ops offers it, which a test checks.
var tiers = map[string]string{
	protocol.MethodServerInfo:            tierRead,
	protocol.MethodRunList:               tierRead,
	protocol.MethodRunGet:                tierRead,
	protocol.MethodRunACPHistory:         tierRead,
	protocol.MethodTerminalHistory:       tierRead,
	protocol.MethodWorkspaceList:         tierRead,
	protocol.MethodWorkspaceGet:          tierRead,
	protocol.MethodWorkspaceTimeline:     tierRead,
	protocol.MethodAgentList:             tierRead,
	protocol.MethodAgentModels:           tierRead,
	protocol.MethodAccountList:           tierRead,
	protocol.MethodTemplateList:          tierRead,
	protocol.MethodChangesList:           tierRead,
	protocol.MethodRunChildren:           tierRead,
	protocol.MethodAccountUsage:          tierRead,
	protocol.MethodTerminalStatus:        tierRead,
	protocol.MethodAssistantDocs:         tierRead,
	protocol.MethodAssistantViewContext:  tierRead,
	protocol.MethodMemorySearch:          tierRead,
	protocol.MethodAttentionList:         tierRead,
	protocol.MethodInsightsGet:           tierRead,
	protocol.MethodMissionList:           tierRead,
	protocol.MethodMissionShow:           tierRead,
	protocol.MethodCoordMessagesList:     tierRead,
	protocol.MethodRunRoomList:           tierRead,
	protocol.MethodRunGitLog:             tierRead,
	protocol.MethodRunGitDiff:            tierRead,
	protocol.MethodRunEvidenceList:       tierRead,
	protocol.MethodRunPRStatus:           tierRead,
	protocol.MethodRunPRFeedback:         tierRead,
	protocol.MethodRunOverlaps:           tierRead,
	protocol.MethodRunLaunch:             tierConfirmed,
	protocol.MethodTemplateLaunch:        tierConfirmed,
	protocol.MethodMissionCreate:         tierConfirmed,
	protocol.MethodRunInject:             tierConfirmed,
	protocol.MethodRunPause:              tierReversible,
	protocol.MethodRunResume:             tierReversible,
	protocol.MethodRunKill:               tierConfirmed,
	protocol.MethodRunClose:              tierConfirmed,
	protocol.MethodEnvSave:               tierConfirmed,
	protocol.MethodRunInputRespond:       tierConfirmed,
	protocol.MethodMissionQuestionAnswer: tierConfirmed,
	protocol.MethodRunRoomPost:           tierConfirmed,

	protocol.MethodMemberList:               tierRead,
	protocol.MethodMemoryShow:               tierRead,
	protocol.MethodMemoryAdd:                tierConfirmed,
	protocol.MethodRunRelaunch:              tierConfirmed,
	protocol.MethodRunModeSwitch:            tierConfirmed,
	protocol.MethodRunProtect:               tierConfirmed,
	protocol.MethodRunArchive:               tierConfirmed,
	protocol.MethodRunHandoff:               tierConfirmed,
	protocol.MethodMissionWorkerRelease:     tierConfirmed,
	protocol.MethodMissionArchive:           tierConfirmed,
	protocol.MethodMissionUnarchive:         tierConfirmed,
	protocol.MethodMissionReplaceIntegrator: tierConfirmed,
	protocol.MethodRunPRCreate:              tierConfirmed,
	protocol.MethodRunPRMerge:               tierConfirmed,
	protocol.MethodMemoryPin:                tierConfirmed,
	protocol.MethodMemoryUnpin:              tierConfirmed,

	protocol.MethodAssistantPreferences: tierRead,
	protocol.MethodAssistantRemember:    tierReversible,
	protocol.MethodAssistantForget:      tierConfirmed,
	protocol.MethodLaunchDefaultsGet:    tierRead,
	protocol.MethodLaunchDefaultsSet:    tierConfirmed,
	protocol.MethodWatchList:            tierRead,
	protocol.MethodWatchGet:             tierRead,
	protocol.MethodWatchCreate:          tierReversible,
	protocol.MethodWatchUpdate:          tierReversible,
	protocol.MethodWatchDelete:          tierConfirmed,
}

// Reversible reports whether the assistant may call method without a card
// although it changes state.
func Reversible(method string) bool { return tiers[method] == tierReversible }

type Caller interface {
	Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error)
}

type Confirmer func(ctx context.Context, c protocol.AssistantConfirmation) error

type Server struct {
	Caller  Caller
	Confirm Confirmer
	// Member is the member the socket belongs to; "mine" in a run.list
	// request selects their runs.
	Member string
	Follow func(ctx context.Context, runID string)
	// View is the member's latest view context.
	View func() protocol.AssistantViewContextResult
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	slots := make(chan struct{}, maxConns)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("assistant tools: accept: %w", err)
		}
		select {
		case slots <- struct{}{}:
		default:
			slog.Warn("assistant tools: connection refused at the concurrency cap", "cap", maxConns)
			_ = conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer func() {
				<-slots
				wg.Done()
			}()
			s.serve(ctx, conn)
		}()
	}
}

func (s *Server) serve(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	capped := &capReader{r: conn}
	r := bufio.NewReaderSize(capped, 16<<10)
	for {
		capped.left = maxRequestBytes
		if conn.SetReadDeadline(time.Now().Add(idleTimeout)) != nil {
			return
		}
		line, err := protocol.ReadLine(r)
		if err != nil {
			return
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		req, resp, ok := protocol.ParseRequest(line)
		if ok {
			result, rpcErr := s.dispatchWatched(ctx, conn, r, req)
			if rpcErr != nil {
				resp.Error = rpcErr
			} else {
				resp.Result = result
			}
		}
		out, err := json.Marshal(resp)
		if err != nil || conn.SetWriteDeadline(time.Now().Add(idleTimeout)) != nil {
			return
		}
		if _, err := conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// dispatchWatched runs req with a context that ends when the caller hangs up,
// so a confirmation nobody can answer any more is withdrawn. The wait for a
// confirmation outlasts the idle deadline, which is lifted meanwhile.
func (s *Server) dispatchWatched(ctx context.Context, conn net.Conn, r *bufio.Reader, req protocol.Request) (json.RawMessage, *protocol.Error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	_ = conn.SetReadDeadline(time.Time{})
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		if _, err := r.Peek(1); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			cancel()
		}
	}()
	result, rpcErr := s.dispatch(ctx, req)
	_ = conn.SetReadDeadline(time.Now())
	<-watching
	return result, rpcErr
}

func (s *Server) dispatch(ctx context.Context, req protocol.Request) (json.RawMessage, *protocol.Error) {
	tier, allowed := tiers[req.Method]
	if !allowed {
		slog.Info("assistant tool", "method", req.Method, "tier", "none", "outcome", "method_not_found")
		return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: fmt.Sprintf(
			"method not found: %s; allowed methods: %s", req.Method, strings.Join(allowedMethods(), ", "))}
	}
	result, rpcErr := s.run(ctx, req, tier)
	outcome := "ok"
	if rpcErr != nil {
		outcome = fmt.Sprintf("error %d", rpcErr.Code)
	}
	slog.Info("assistant tool", "method", req.Method, "tier", tier, "outcome", outcome)
	return result, rpcErr
}

func (s *Server) run(ctx context.Context, req protocol.Request, tier string) (json.RawMessage, *protocol.Error) {
	params, reason, defaults, err := statedReason(req.Method, req.Params)
	if err == nil {
		params, err = Prepare(s.Member, req.Method, params)
	}
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
	}
	req.Params = params
	switch {
	case req.Method == protocol.MethodAssistantDocs:
		return serveDocs(req.Params)
	case req.Method == protocol.MethodAssistantViewContext:
		return s.serveView()
	case tier == tierConfirmed:
		if result, ok := s.replayTemplateLaunch(ctx, req); ok {
			return result, nil
		}
		c := protocol.AssistantConfirmation{Method: req.Method, Params: req.Params, CreatedAt: time.Now().UTC(), Defaults: defaults}
		if rpcErr := s.summarize(ctx, req, &c); rpcErr != nil {
			return nil, rpcErr
		}
		req.Params = c.Params
		c.Summary += defaultsNote(defaults)
		if reason != "" {
			c.Reason = reason
			c.Summary += fmt.Sprintf(". The assistant's reason: %q", reason)
		}
		if err := s.Confirm(ctx, c); err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) {
				return nil, pe
			}
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: req.Method + " was not approved: " + err.Error()}
		}
	}
	result, rpcErr := s.Caller.Call(ctx, req.Method, req.Params)
	if rpcErr == nil && s.Follow != nil {
		if id := s.followed(req, result); id != "" {
			s.Follow(context.WithoutCancel(ctx), id)
		}
	}
	return result, rpcErr
}

// replayTemplateLaunch returns the run a keyed template.launch already
// started, without a card: a retry after a lost reply must get it even when
// the template has since changed or gone, which the card would refuse. It
// launches nothing, so a key with no run falls through to the card.
func (s *Server) replayTemplateLaunch(ctx context.Context, req protocol.Request) (json.RawMessage, bool) {
	if req.Method != protocol.MethodTemplateLaunch {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(req.Params, &fields) != nil || len(fields["idempotency_key"]) == 0 || string(fields["idempotency_key"]) == `""` {
		return nil, false
	}
	fields["replay_only"] = json.RawMessage("true")
	params, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	result, rpcErr := s.Caller.Call(ctx, req.Method, params)
	if rpcErr != nil {
		return nil, false
	}
	if s.Follow != nil {
		if id := s.followed(req, result); id != "" {
			s.Follow(context.WithoutCancel(ctx), id)
		}
	}
	return result, true
}

func (s *Server) followed(req protocol.Request, result json.RawMessage) string {
	switch req.Method {
	case protocol.MethodRunLaunch, protocol.MethodTemplateLaunch, protocol.MethodRunRelaunch:
		launched, err := protocol.DecodeParams[protocol.RunResult](result)
		if err != nil {
			slog.Warn("assistant tools: read the launched run", "error", err)
			return ""
		}
		return launched.Run.ID
	case protocol.MethodMissionCreate:
		created, err := protocol.DecodeParams[protocol.MissionCreateResult](result)
		if err != nil {
			slog.Warn("assistant tools: read the created swarm", "error", err)
			return ""
		}
		return created.Mission.CurrentIntegratorRunID
	case protocol.MethodMissionReplaceIntegrator:
		replaced, err := protocol.DecodeParams[protocol.MissionReplaceIntegratorResult](result)
		if err != nil {
			slog.Warn("assistant tools: read the replacement integrator", "error", err)
			return ""
		}
		return replaced.RunID
	case protocol.MethodRunInject:
		p, err := protocol.DecodeParams[protocol.RunInjectParams](req.Params)
		if err != nil {
			return ""
		}
		return p.RunID
	}
	return ""
}

func (s *Server) summarize(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	switch req.Method {
	case protocol.MethodEnvSave:
		c.Summary = "Save your environment as a new image; new runs and terminals start from it"
		return nil
	case protocol.MethodRunLaunch:
		return s.summarizeLaunch(ctx, req, c)
	case protocol.MethodMissionCreate:
		return s.summarizeMission(ctx, req, c)
	case protocol.MethodTemplateLaunch:
		return s.summarizeTemplate(ctx, req, c)
	case protocol.MethodRunInject:
		return s.summarizeInject(ctx, req, c)
	case protocol.MethodRunKill:
		p, err := protocol.DecodeParams[protocol.RunIDParams](req.Params)
		if err != nil || p.RunID == "" {
			return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
		}
		run, rpcErr := s.ownedRun(ctx, p.RunID, c)
		if rpcErr != nil {
			return rpcErr
		}
		c.Summary = fmt.Sprintf("Stop run %s (%q)%s", p.RunID, runLabel(run), ownedBy(c))
		return nil
	case protocol.MethodRunInputRespond:
		return s.summarizeRespond(ctx, req, c)
	case protocol.MethodMissionQuestionAnswer:
		return s.summarizeSwarmAnswer(ctx, req, c)
	case protocol.MethodRunRoomPost:
		p, err := protocol.DecodeParams[protocol.RunRoomPostParams](req.Params)
		if err != nil || p.RunID == "" {
			return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
		}
		run, rpcErr := s.ownedRun(ctx, p.RunID, c)
		if rpcErr != nil {
			return rpcErr
		}
		verb := "Reply on"
		if p.Kind != protocol.RoomMessageReply {
			verb = fmt.Sprintf("Post a %q message on", truncate(string(p.Kind)))
		}
		c.Summary = fmt.Sprintf("%s run %s (%q)%s: %q%s", verb, p.RunID, runLabel(run), ownedBy(c), truncate(p.Body), attached(p.Attachments, p.Anchor))
		return nil
	}
	if summary, ok := actSummaries[req.Method]; ok {
		return summary(s, ctx, req, c)
	}
	return &protocol.Error{Code: protocol.CodeInternal, Message: req.Method + ": no confirmation summary for this method"}
}

// summarizeRespond names the run, its request and the option chosen, from
// the request as the Needs you list shows it.
func (s *Server) summarizeRespond(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.RunInputRespondParams](req.Params)
	if err != nil || p.RunID == "" || p.RequestID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id and request_id are required"}
	}
	run, rpcErr := s.ownedRun(ctx, p.RunID, c)
	if rpcErr != nil {
		return rpcErr
	}
	items, rpcErr := s.attention(ctx, run.WorkspaceID)
	if rpcErr != nil {
		return rpcErr
	}
	for _, it := range items {
		if it.RunID != p.RunID || it.Request == nil || it.Request.ID != p.RequestID {
			continue
		}
		// The option's label is the agent's own; its kind is what the
		// answer grants, so the card names it, "always" included.
		verb, option := "Answer", p.OptionID
		for _, o := range it.Request.Options {
			if o.ID == p.OptionID {
				option = truncate(cmp.Or(o.Label, o.ID))
				switch o.Kind {
				case "allow_once":
					verb = "Allow once"
				case "allow_always":
					verb = "Allow always"
				case "reject_once":
					verb = "Deny once"
				case "reject_always":
					verb = "Deny always"
				}
			}
		}
		c.Summary = fmt.Sprintf("%s run %s's request (%q)%s: %q, choosing %q", verb, p.RunID, runLabel(run), ownedBy(c), truncate(it.Request.Title), option)
		if len(p.Values) > 0 {
			values, _ := json.Marshal(p.Values)
			c.Summary += fmt.Sprintf(" with %q", truncate(string(values)))
		}
		return nil
	}
	return &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: run %s has no pending request %s", req.Method, p.RunID, p.RequestID)}
}

// summarizeSwarmAnswer names the swarm whose integrator asked, by its
// objective, and its accountable human when that is another member.
func (s *Server) summarizeSwarmAnswer(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.MissionQuestionAnswerParams](req.Params)
	if err != nil || p.QuestionID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": question_id is required"}
	}
	var shown protocol.MissionShowResult
	if rpcErr := s.call(ctx, protocol.MethodMissionShow, protocol.MissionShowParams{QuestionID: p.QuestionID}, &shown); rpcErr != nil {
		return rpcErr
	}
	i := slices.IndexFunc(shown.Questions, func(q protocol.MissionQuestion) bool { return q.ID == p.QuestionID })
	if i < 0 {
		return &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: swarm %s has no question %s", req.Method, shown.Mission.ID, p.QuestionID)}
	}
	if rpcErr := s.owner(ctx, shown.Mission.AccountableHumanID, c); rpcErr != nil {
		return rpcErr
	}
	q := shown.Questions[i]
	again, waits := "", ""
	if q.AnsweredAt != nil {
		again, waits = " again", " and no longer waits"
	}
	c.Summary = fmt.Sprintf("Answer swarm %s (%q)%s%s, which asked %q%s: %q", shown.Mission.ID, truncate(shown.Mission.Objective), ownedBy(c), again,
		truncate(q.Body), waits, truncate(p.Answer))
	return nil
}

func (s *Server) attention(ctx context.Context, workspaceID string) ([]protocol.AttentionItem, *protocol.Error) {
	var res protocol.AttentionListResult
	rpcErr := s.call(ctx, protocol.MethodAttentionList, protocol.AttentionListParams{WorkspaceID: workspaceID}, &res)
	return res.Items, rpcErr
}

// ownedRun is the run id names, with c marked as acting on another
// member's run when it is one.
func (s *Server) ownedRun(ctx context.Context, id string, c *protocol.AssistantConfirmation) (protocol.Run, *protocol.Error) {
	params, _ := json.Marshal(protocol.RunIDParams{RunID: id})
	run, rpcErr := s.getRun(ctx, params)
	if rpcErr != nil {
		return protocol.Run{}, rpcErr
	}
	return run, s.owner(ctx, run.MemberID, c)
}

func (s *Server) call(ctx context.Context, method string, params, out any) *protocol.Error {
	raw, err := json.Marshal(params)
	if err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: method + ": " + err.Error()}
	}
	result, rpcErr := s.Caller.Call(ctx, method, raw)
	if rpcErr != nil {
		return rpcErr
	}
	if err := json.Unmarshal(result, out); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "decode " + method + " result: " + err.Error()}
	}
	return nil
}

// launchNames names the accounts and repositories a launch's card
// mentions; shared are the teammates' accounts shared with the member, and
// savedAccount the member's saved account when the launch was filled from
// it.
type launchNames struct {
	member               string
	accounts, workspaces map[string]string
	shared               []string
	savedAccount         string
}

func (s *Server) launchNames(ctx context.Context, defaults *protocol.AssistantDefaultsUse) (launchNames, *protocol.Error) {
	var accounts protocol.AccountListResult
	if rpcErr := s.call(ctx, protocol.MethodAccountList, nil, &accounts); rpcErr != nil {
		return launchNames{}, rpcErr
	}
	var workspaces protocol.WorkspaceListResult
	if rpcErr := s.call(ctx, protocol.MethodWorkspaceList, nil, &workspaces); rpcErr != nil {
		return launchNames{}, rpcErr
	}
	n := launchNames{member: s.Member, accounts: map[string]string{}, workspaces: map[string]string{}}
	if defaults != nil && slices.ContainsFunc(defaults.Applied, func(f string) bool { return f == "account_member_id" || f == "workers.account_member_id" }) {
		var saved protocol.LaunchDefaultsResult
		if rpcErr := s.call(ctx, protocol.MethodLaunchDefaultsGet, nil, &saved); rpcErr != nil {
			return launchNames{}, rpcErr
		}
		n.savedAccount = saved.Defaults.AccountMemberID
	}
	for _, m := range accounts.Accounts {
		n.accounts[m.ID] = cmp.Or(m.DisplayName, m.ID)
		if m.ID != s.Member {
			n.shared = append(n.shared, m.ID)
		}
	}
	for _, w := range workspaces.Workspaces {
		n.workspaces[w.ID] = w.Name
	}
	return n, nil
}

func (n launchNames) owner(id string) string {
	return truncate(cmp.Or(n.accounts[id], id))
}

// account is whose account id is. A shared account the launch names does
// not mark the card: the member chose it, in this launch or in their saved
// defaults, and the server refuses one not shared with them.
func (n launchNames) account(id string) string {
	if id == "" || id == n.member {
		return "your account"
	}
	if id == n.savedAccount {
		return fmt.Sprintf("%q's account (your saved default)", n.owner(id))
	}
	return fmt.Sprintf("%q's account", n.owner(id))
}

// maxNamed bounds how many accounts, repositories or agents a card names.
const maxNamed = 6

func named(items []string) string {
	shown := items[:min(len(items), maxNamed)]
	if more := len(items) - len(shown); more > 0 {
		shown = append(slices.Clone(shown), fmt.Sprintf("%d more", more))
	}
	return strings.Join(shown, ", ")
}

// options names a launch's fallback order and extra repositories. Automatic
// fallback while any account is shared with the member marks c as their
// owners', since the launch may spend usage nobody chose, even when the
// member saved automatic fallback: it also covers accounts shared after the
// save. An explicit order does not.
func (n launchNames) options(order []string, off bool, extras []string, c *protocol.AssistantConfirmation) string {
	var out string
	switch {
	case off:
		out = ", no fallback"
	case len(order) == 0 && len(n.shared) > 0:
		out = ", may fall back to accounts shared with you"
		if !c.OtherMember {
			owners := make([]string, len(n.shared))
			for i, id := range n.shared {
				owners[i] = n.owner(id)
			}
			c.OtherMember, c.OtherOwner = true, named(owners)
		}
	case len(order) > 0:
		accounts := make([]string, len(order))
		for i, id := range order {
			accounts[i] = n.account(id)
		}
		out = ", falling back to " + named(accounts) + " in order"
	}
	if len(extras) > 0 {
		repos := make([]string, len(extras))
		for i, id := range extras {
			repos[i] = fmt.Sprintf("%q", truncate(cmp.Or(n.workspaces[id], id)))
		}
		out += ", with repositories " + named(repos)
	}
	return out
}

func (s *Server) summarizeLaunch(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.RunLaunchParams](req.Params)
	if err != nil || p.WorkspaceID == "" || p.Harness == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": workspace_id and harness are required"}
	}
	ws, rpcErr := s.workspaceName(ctx, p.WorkspaceID)
	if rpcErr != nil {
		return rpcErr
	}
	n, rpcErr := s.launchNames(ctx, c.Defaults)
	if rpcErr != nil {
		return rpcErr
	}
	mode := p.Mode
	if mode == "" {
		mode = "default mode"
	}
	summary := fmt.Sprintf("Launch %s (%s) in %s", p.Harness, mode, ws)
	if p.AccountMemberID != "" && p.AccountMemberID != s.Member {
		summary += " on " + n.account(p.AccountMemberID)
	}
	summary += n.options(p.FallbackAccountMemberIDs, p.NoFallback, p.ExtraWorkspaceIDs, c)
	if p.Children != "" {
		summary += ", child runs " + p.Children
	}
	c.Summary = fmt.Sprintf("%s: \"%s\"", summary, firstLine(p.Task))
	return nil
}

func (s *Server) summarizeMission(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.MissionCreateParams](req.Params)
	if err != nil || p.WorkspaceID == "" || p.Integrator.Harness == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": workspace_id and integrator are required"}
	}
	ws, rpcErr := s.workspaceName(ctx, p.WorkspaceID)
	if rpcErr != nil {
		return rpcErr
	}
	n, rpcErr := s.launchNames(ctx, c.Defaults)
	if rpcErr != nil {
		return rpcErr
	}
	agents := make([]string, len(p.ExecutionChoices))
	for i, choice := range p.ExecutionChoices {
		agents[i] = fmt.Sprintf("%s (%s) on %s", truncate(choice.Harness), choice.Mode, n.account(choice.AccountMemberID))
	}
	c.Summary = fmt.Sprintf("Start a swarm in %s with integrator %s (%s) on %s, agents %s%s: \"%s\"", ws, truncate(p.Integrator.Harness), p.Integrator.Mode,
		n.account(p.Integrator.AccountMemberID), named(agents), n.options(p.FallbackAccountMemberIDs, p.NoFallback, p.ExtraWorkspaceIDs, c), firstLine(p.Objective))
	return nil
}

// summarizeTemplate names the template's agent, mode, model and task as the
// params fill it in, and pins the launch to that version of the template,
// which any workspace admin may change while the card waits.
func (s *Server) summarizeTemplate(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.TemplateLaunchParams](req.Params)
	if err != nil || p.WorkspaceID == "" || p.Name == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": workspace_id and name are required"}
	}
	ws, rpcErr := s.workspaceName(ctx, p.WorkspaceID)
	if rpcErr != nil {
		return rpcErr
	}
	var list protocol.TemplateListResult
	if rpcErr := s.call(ctx, protocol.MethodTemplateList, protocol.TemplateListParams{WorkspaceID: p.WorkspaceID}, &list); rpcErr != nil {
		return rpcErr
	}
	i := slices.IndexFunc(list.Templates, func(t protocol.Template) bool { return t.Name == p.Name })
	if i < 0 {
		return &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: workspace %s has no template %q", req.Method, ws, truncate(p.Name))}
	}
	t := list.Templates[i]
	if p.ExpectedVersion != "" && p.ExpectedVersion != t.Version {
		return &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("%s: template %q was saved again and is now version %s, not %s",
			req.Method, truncate(p.Name), t.Version, truncate(p.ExpectedVersion))}
	}
	task, err := templates.Render(t.Task, t.Params, p.Params)
	if err != nil {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(req.Params, &fields); err != nil {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": params must be an object"}
	}
	fields["expected_version"], _ = json.Marshal(t.Version)
	if c.Params, err = json.Marshal(fields); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: req.Method + ": " + err.Error()}
	}
	agent := fmt.Sprintf("%s (%s)", truncate(t.Harness), cmp.Or(t.Mode, "headless"))
	if t.Model != "" {
		agent = fmt.Sprintf("%s (%s, %s)", truncate(t.Harness), cmp.Or(t.Mode, "headless"), truncate(t.Model))
	}
	c.Task = task
	c.Summary = fmt.Sprintf("Launch template %q in %s: %s on your account: \"%s\"", truncate(p.Name), ws, agent, firstLine(task))
	return nil
}

// maxReason bounds the assistant's stated reason on a card.
const maxReason = 200

// savedApplied are the launch fields ops may say saved defaults filled, and
// savedSkippable those it may say it skipped or could not check.
var (
	savedApplied   = []string{"harness", "mode", "model", "account_member_id", "fallback", "workers.account_member_id"}
	savedSkippable = []string{"harness", "mode", "model", "fallback_account_member_ids"}
)

// statedReason takes the assistant's one-line reason for a launch, and
// what saved defaults filled into it, off its params: they are for the
// member's card, and the server refuses params it does not know. The
// assistant writes both, so saved_defaults may name only launch fields, and
// its reasons are cut to fit a card.
func statedReason(method string, params json.RawMessage) (json.RawMessage, string, *protocol.AssistantDefaultsUse, error) {
	if !slices.Contains([]string{protocol.MethodRunLaunch, protocol.MethodMissionCreate, protocol.MethodTemplateLaunch}, method) || len(params) == 0 || string(params) == "null" {
		return params, "", nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, "", nil, errors.New("params must be an object")
	}
	rawReason, stated := fields["reason"]
	rawDefaults, noted := fields["saved_defaults"]
	if !stated && !noted {
		return params, "", nil, nil
	}
	var reason string
	if stated {
		if err := json.Unmarshal(rawReason, &reason); err != nil {
			return nil, "", nil, errors.New("reason must be a string")
		}
	}
	var defaults *protocol.AssistantDefaultsUse
	if noted {
		var err error
		if defaults, err = savedUse(rawDefaults); err != nil {
			return nil, "", nil, err
		}
	}
	delete(fields, "reason")
	delete(fields, "saved_defaults")
	out, err := json.Marshal(fields)
	if r := []rune(reason); len(r) > maxReason {
		reason = string(r[:maxReason]) + "…"
	}
	return out, reason, defaults, err
}

func savedUse(raw json.RawMessage) (*protocol.AssistantDefaultsUse, error) {
	var d *protocol.AssistantDefaultsUse
	if err := json.Unmarshal(raw, &d); err != nil || d == nil {
		return nil, errors.New("saved_defaults must be the fields saved defaults filled, skipped and left unchecked")
	}
	for _, field := range d.Applied {
		if !slices.Contains(savedApplied, field) {
			return nil, fmt.Errorf("saved_defaults.applied names %q, which is not one of %s", truncate(field), strings.Join(savedApplied, ", "))
		}
	}
	d.Applied = d.Applied[:min(len(d.Applied), len(savedApplied))]
	notes := func(list []protocol.AssistantDefaultSkip, name string) ([]protocol.AssistantDefaultSkip, error) {
		list = list[:min(len(list), maxNamed)]
		for i, n := range list {
			if !slices.Contains(savedSkippable, n.Field) {
				return nil, fmt.Errorf("saved_defaults.%s names %q, which is not one of %s", name, truncate(n.Field), strings.Join(savedSkippable, ", "))
			}
			list[i].Reason = truncate(n.Reason)
		}
		return list, nil
	}
	var err error
	if d.Skipped, err = notes(d.Skipped, "skipped"); err != nil {
		return nil, err
	}
	if d.Unchecked, err = notes(d.Unchecked, "unchecked"); err != nil {
		return nil, err
	}
	return d, nil
}

// defaultsNote says on a launch card which fields saved defaults filled,
// which saved choices the launch skipped and why, and which it could not
// check on the launch's account.
func defaultsNote(d *protocol.AssistantDefaultsUse) string {
	if d == nil {
		return ""
	}
	var parts []string
	if len(d.Applied) > 0 {
		parts = append(parts, "From your saved defaults: "+strings.Join(d.Applied, ", "))
	}
	for _, skip := range d.Skipped {
		parts = append(parts, fmt.Sprintf("Saved %s not used: %s", skip.Field, skip.Reason))
	}
	for _, n := range d.Unchecked {
		parts = append(parts, fmt.Sprintf("Saved %s not checked: %s", n.Field, n.Reason))
	}
	if len(parts) == 0 {
		return ""
	}
	return ". " + strings.Join(parts, ". ")
}

func (s *Server) workspaceName(ctx context.Context, id string) (string, *protocol.Error) {
	var ws protocol.WorkspaceGetResult
	if rpcErr := s.call(ctx, protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: id}, &ws); rpcErr != nil {
		return "", rpcErr
	}
	return ws.Workspace.Name, nil
}

func (s *Server) summarizeInject(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.RunInjectParams](req.Params)
	if err != nil || p.RunID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
	}
	run, rpcErr := s.ownedRun(ctx, p.RunID, c)
	if rpcErr != nil {
		return rpcErr
	}
	c.Summary = fmt.Sprintf("Send to run %s (%q)%s: %q%s", p.RunID, runLabel(run), ownedBy(c), truncate(p.Message), attached(p.Attachments, nil))
	return nil
}

// owner marks c as acting on another member's run or swarm when member is
// not the socket's member, and names that member: their display name, else
// their id.
func (s *Server) owner(ctx context.Context, member string, c *protocol.AssistantConfirmation) *protocol.Error {
	if member == s.Member {
		return nil
	}
	c.OtherMember, c.OtherOwner = true, member
	raw, rpcErr := s.Caller.Call(ctx, protocol.MethodMemberList, nil)
	if rpcErr != nil {
		return rpcErr
	}
	var members protocol.MemberListResult
	if err := json.Unmarshal(raw, &members); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "decode member.list result: " + err.Error()}
	}
	for _, m := range members.Members {
		if m.ID == member && m.DisplayName != "" {
			c.OtherOwner = m.DisplayName
		}
	}
	return nil
}

// attached names a message's attachments and the place it is anchored to.
func attached(files []string, anchor *protocol.RoomMessageAnchor) string {
	var out string
	if len(files) > 0 {
		quoted := make([]string, len(files))
		for i, f := range files {
			quoted[i] = fmt.Sprintf("%q", truncate(f))
		}
		out = ", attaching " + strings.Join(quoted, ", ")
	}
	if anchor == nil {
		return out
	}
	switch {
	case anchor.Path != "":
		at := anchor.Path
		if anchor.StartLine > 0 {
			at += fmt.Sprintf(":%d", anchor.StartLine)
		}
		if anchor.EndLine > anchor.StartLine {
			at += fmt.Sprintf("-%d", anchor.EndLine)
		}
		out += fmt.Sprintf(", anchored at %q", truncate(at))
	case anchor.Kind != "":
		out += fmt.Sprintf(", anchored at %s offset %d", truncate(anchor.Kind), anchor.TranscriptOffset)
	}
	return out
}

func ownedBy(c *protocol.AssistantConfirmation) string {
	if !c.OtherMember {
		return ""
	}
	return ", owned by " + c.OtherOwner
}

func (s *Server) getRun(ctx context.Context, params json.RawMessage) (protocol.Run, *protocol.Error) {
	raw, rpcErr := s.Caller.Call(ctx, protocol.MethodRunGet, params)
	if rpcErr != nil {
		return protocol.Run{}, rpcErr
	}
	var got protocol.RunResult
	if err := json.Unmarshal(raw, &got); err != nil {
		return protocol.Run{}, &protocol.Error{Code: protocol.CodeInternal, Message: "decode run.get result: " + err.Error()}
	}
	return got.Run, nil
}

func runLabel(run protocol.Run) string {
	if run.Title != "" {
		return run.Title
	}
	return run.Task
}

const maxSummaryText = 120

// firstLine is text's first line, cut to maxSummaryText, ending in "…" when
// anything was left out, so a card quotes a task without escapes.
func firstLine(text string) string {
	line, rest, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > maxSummaryText {
		return string(r[:maxSummaryText]) + "…"
	}
	if rest != "" {
		return line + "…"
	}
	return line
}

func truncate(text string) string {
	if r := []rune(text); len(r) > maxSummaryText {
		return string(r[:maxSummaryText]) + "…"
	}
	return text
}

// Prepare checks the params of a call made for member, by the assistant's
// socket or a connector. Either is bound to one member, so the wire never
// names one as the actor. It may name the agent accounts a call runs on or
// reads, the accountFields, which the server checks member may use;
// run.list takes member_id as a read filter, where "mine" selects member;
// and run.handoff names the teammate it hands the run to. mission.create
// makes member the swarm's accountable human and the account of every agent
// that names none.
func Prepare(member, method string, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 || string(params) == "null" {
		return params, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, errors.New("params must be an object")
	}
	for name := range fields {
		if strings.Contains(strings.ToLower(name), "member_id") && !slices.Contains(accountFields[method], name) &&
			(method != protocol.MethodRunList || name != "member_id") && (method != protocol.MethodRunHandoff || name != "to_member_id") {
			return nil, fmt.Errorf("%s is not accepted: calls act as the member this socket belongs to", name)
		}
	}
	switch method {
	case protocol.MethodMissionCreate:
		return prepareMission(member, params)
	case protocol.MethodRunList:
	default:
		return params, nil
	}
	var mine bool
	if raw, ok := fields["mine"]; ok {
		if err := json.Unmarshal(raw, &mine); err != nil {
			return nil, errors.New("mine must be a boolean")
		}
		delete(fields, "mine")
	}
	if mine {
		id, _ := json.Marshal(member)
		fields["member_id"] = id
	}
	return json.Marshal(fields)
}

// accountFields are the params, by method, that name an agent account to
// launch on or read; the server refuses one not shared with the caller.
var accountFields = map[string][]string{
	protocol.MethodRunLaunch:     {"account_member_id", "fallback_account_member_ids"},
	protocol.MethodMissionCreate: {"fallback_account_member_ids"},
	protocol.MethodAgentList:     {"account_member_id"},
	protocol.MethodAgentModels:   {"account_member_id"},
	protocol.MethodAccountUsage:  {"account_member_id"},
}

// prepareMission refuses fields mission.create does not know, as the server
// does: re-encoding p would drop them, so a misspelled option, such as
// no_fallback, would launch without it.
func prepareMission(member string, params json.RawMessage) (json.RawMessage, error) {
	var p protocol.MissionCreateParams
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			return nil, fmt.Errorf("unknown param %s; this server does not support it", field)
		}
		return nil, err
	}
	if p.AccountableHumanID != "" {
		return nil, errors.New("accountable_human_id is not accepted: calls act as the member this socket belongs to")
	}
	p.AccountableHumanID = member
	p.Integrator.AccountMemberID = cmp.Or(p.Integrator.AccountMemberID, member)
	for i := range p.ExecutionChoices {
		p.ExecutionChoices[i].AccountMemberID = cmp.Or(p.ExecutionChoices[i].AccountMemberID, member)
	}
	return json.Marshal(p)
}

func serveDocs(raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	p, err := protocol.DecodeParams[protocol.AssistantDocsParams](raw)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "assistant.docs: " + err.Error()}
	}
	topics, err := docTopics()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "assistant.docs: " + err.Error()}
	}
	var out protocol.AssistantDocsResult
	switch {
	case p.Topic == "":
		out.Topics = topics
	case slices.Contains(topics, p.Topic):
		body, rerr := fs.ReadFile(docs.FS, p.Topic+".md")
		if rerr != nil {
			return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "assistant.docs: " + rerr.Error()}
		}
		out.Text = string(body)
	default:
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf(
			"assistant.docs: unknown topic %q; topics: %s", p.Topic, strings.Join(topics, ", "))}
	}
	result, err := json.Marshal(out)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "assistant.docs: " + err.Error()}
	}
	return result, nil
}

func (s *Server) serveView() (json.RawMessage, *protocol.Error) {
	var out protocol.AssistantViewContextResult
	if s.View != nil {
		out = s.View()
	}
	result, err := json.Marshal(out)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "assistant.view_context: " + err.Error()}
	}
	return result, nil
}

func docTopics() ([]string, error) {
	entries, err := fs.ReadDir(docs.FS, ".")
	if err != nil {
		return nil, err
	}
	topics := make([]string, 0, len(entries))
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".md"); ok {
			topics = append(topics, name)
		}
	}
	return topics, nil
}

func allowedMethods() []string {
	names := make([]string, 0, len(tiers))
	for name := range tiers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// capReader fails the read past left bytes so one oversized line cannot
// make the server buffer it.
type capReader struct {
	r    io.Reader
	left int
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, fmt.Errorf("assistant tools: request exceeds %d bytes", maxRequestBytes)
	}
	if len(p) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= n
	return n, err
}

// OwnerName names the member who owns run when it is not member: their
// display name, else their id. It is empty for member's own run.
func OwnerName(ctx context.Context, c Caller, member, run string) (string, *protocol.Error) {
	var conf protocol.AssistantConfirmation
	if _, rpcErr := (&Server{Caller: c, Member: member}).ownedRun(ctx, run, &conf); rpcErr != nil {
		return "", rpcErr
	}
	return conf.OtherOwner, nil
}
