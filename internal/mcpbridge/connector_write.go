package mcpbridge

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// duplicateWindow is how long a launch with no idempotency key returns an
// identical launch of the member's instead of launching again.
const duplicateWindow = 60 * time.Second

type connectorLaunchInput struct {
	WorkspaceID    string `json:"workspace_id"`
	Task           string `json:"task" jsonschema:"a complete brief"`
	Harness        string `json:"harness" jsonschema:"agent name"`
	Mode           string `json:"mode,omitempty" jsonschema:"acp is enhanced; default the agent's"`
	Model          string `json:"model,omitempty" jsonschema:"as the agent names it; default its own"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type swarmWorker struct {
	Harness string `json:"harness"`
	Mode    string `json:"mode,omitempty" jsonschema:"default tui"`
	Model   string `json:"model,omitempty"`
}

type launchSwarmInput struct {
	WorkspaceID    string        `json:"workspace_id"`
	Objective      string        `json:"objective" jsonschema:"the goal, as a complete brief"`
	Harness        string        `json:"harness" jsonschema:"the integrator's agent"`
	Mode           string        `json:"mode,omitempty" jsonschema:"default tui"`
	Model          string        `json:"model,omitempty"`
	Workers        []swarmWorker `json:"workers,omitempty" jsonschema:"agents workers may use; default the integrator's"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
}

type connectorMessageInput struct {
	RunID          string `json:"run_id"`
	Message        string `json:"message"`
	Steer          bool   `json:"steer,omitempty" jsonschema:"add to an enhanced run's running turn"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type answerInput struct {
	RunID    string         `json:"run_id"`
	AnswerID string         `json:"answer_id" jsonschema:"from aether_needs_attention"`
	Answer   string         `json:"answer,omitempty" jsonschema:"reply text, for a swarm or teammate question"`
	OptionID string         `json:"option_id,omitempty" jsonschema:"for an agent's question, one of its options"`
	Values   map[string]any `json:"values,omitempty" jsonschema:"form values for the question's schema"`
}

type reviewInput struct {
	RunID  string `json:"run_id"`
	Action string `json:"action"`
}

type decideInput struct {
	RunID    string `json:"run_id"`
	AnswerID string `json:"answer_id" jsonschema:"from aether_needs_attention"`
	Decision string `json:"decision"`
	Action   string `json:"action" jsonschema:"the item's untrusted.text, word for word"`
}

type closeInput struct {
	RunID   string `json:"run_id"`
	Outcome string `json:"outcome"`
}

const idempotencyRule = " Pass a new idempotency_key and reuse it on a retry; "

var writeTools = []connectorTool{
	connectorToolOf("aether_launch_run", "Launch run", ScopeWrite, false, nextWorkspaces,
		"Use this when the user asks to start an agent on a task. Take workspace_id and harness from aether_list_workspaces and aether_list_agents, then poll aether_get_run. "+
			"For several independent tasks, call it once per task, each with its own idempotency_key."+
			idempotencyRule+"without one, the same launch within 60s returns the first run.", launchRun).
		enum("mode", "tui", "headless", "acp"),
	connectorToolOf("aether_launch_swarm", "Launch swarm", ScopeWrite, false, nextWorkspaces,
		"Use this when the user asks for a swarm or for one goal to be split across agents: an integrator agent plans it into tasks for worker agents. Poll aether_get_swarm."+
			idempotencyRule+"without one, the same objective within 60s returns the first swarm.", launchSwarm).
		enum("mode", "tui", "acp").enum("workers.mode", "tui", "headless", "acp"),
	connectorToolOf("aether_message_run", "Message run", ScopeWrite, false, nextRuns,
		"Use this when the user wants to tell a run's agent something. A message to a run someone else controls waits about 45s for them to approve it."+
			idempotencyRule+"without one, a retry sends it again.", messageRun),
	connectorToolOf("aether_answer", "Answer question", ScopeWrite, true, nextAttention,
		"Use this when an aether_needs_attention item's answer_with is aether_answer: an agent's question takes option_id (and values), a swarm or teammate question takes answer.", answer),
	connectorToolOf("aether_pause_run", "Pause run", ScopeWrite, true, nextRuns,
		"Use this when the user wants a run paused; aether_resume_run continues it.", pauseRun(protocol.MethodRunPause, "Paused")),
	connectorToolOf("aether_resume_run", "Resume run", ScopeWrite, true, nextRuns,
		"Use this when the user wants a paused run to continue.", pauseRun(protocol.MethodRunResume, "Resumed")),
	connectorToolOf("aether_mark_reviewed", "Mark run reviewed", ScopeWrite, true, nextRuns,
		"Use this when the user has checked a run's reported success: complete accepts it and closes the run, later puts it off.", markReviewed).
		enum("action", protocol.RunReviewComplete, protocol.RunReviewLater),
}

var controlTools = []connectorTool{
	connectorToolOf("aether_decide_approval", "Decide approval", ScopeControl, true, nextAttention,
		"Use this when the user allows or denies an approval or permission item from aether_needs_attention. It allows once, never always.", decideApproval).
		enum("decision", "allow", "deny"),
	connectorToolOf("aether_stop_run", "Stop run", ScopeControl, true, nextRuns,
		"Use this when the user wants a run stopped now.", stopRun),
	connectorToolOf("aether_close_run", "Close run", ScopeControl, true, nextRuns,
		"Use this when a run's work was merged or abandoned, to close the run with that outcome.", closeRun).
		enum("outcome", "merged", "abandoned"),
	connectorToolOf("aether_cancel_swarm", "Cancel swarm", ScopeControl, true, nextSwarms,
		"Use this when the user wants a swarm ended; it stops its integrator and workers.", cancelSwarm),
}

func launched(r protocol.Run, l links) map[string]any {
	return map[string]any{"run_id": r.ID, "state": r.Status, "workspace_id": r.WorkspaceID, "harness": r.Harness, "url": l.run(r.ID),
		"next": "poll aether_get_run with run_id"}
}

func recent(at string, now time.Time) bool {
	created, err := time.Parse(time.RFC3339, at)
	return err == nil && now.Sub(created) < duplicateWindow
}

// launchLocks holds a mutex per member id. A member's keyless launch holds
// it from the duplicate check through the launch, so two overlapping
// retries through this server cannot both launch.
var launchLocks sync.Map

func lockLaunches(member string) func() {
	mu, _ := launchLocks.LoadOrStore(member, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

// recentLaunch is the member's run of the same task, agent, mode and
// model in the workspace created within duplicateWindow that has not
// stopped, if any. An omitted mode or model matches any.
func recentLaunch(ctx context.Context, read Call, in connectorLaunchInput, now time.Time) (protocol.Run, bool, error) {
	params := struct {
		protocol.RunListParams
		Mine bool `json:"mine"`
	}{protocol.RunListParams{WorkspaceID: in.WorkspaceID}, true}
	var res protocol.RunListResult
	if err := read(ctx, protocol.MethodRunList, params, &res); err != nil {
		return protocol.Run{}, false, err
	}
	for _, r := range res.Runs {
		if r.Harness == in.Harness && r.Task == in.Task && (in.Mode == "" || r.Mode == in.Mode) && (in.Model == "" || r.Model == in.Model) &&
			!domain.RunStatus(r.Status).Final() && recent(r.CreatedAt, now) {
			return r, true, nil
		}
	}
	return protocol.Run{}, false, nil
}

func launchRun(ctx context.Context, c calls, in connectorLaunchInput) (string, any, error) {
	info, err := serverInfo(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	l := links(info.DashboardURL)
	if in.IdempotencyKey == "" {
		defer lockLaunches(info.Member.ID)()
		r, dup, err := recentLaunch(ctx, c.read, in, time.Now())
		if err != nil {
			return "", nil, err
		}
		if dup {
			out := launched(r, l)
			out["duplicate"] = true
			return fmt.Sprintf("You launched this task with %s at %s; returning run %s instead of launching it again. Pass an idempotency_key to launch another.",
				r.Harness, r.CreatedAt, r.ID), out, nil
		}
	}
	var res protocol.RunLaunchResult
	if err := c.act(ctx, protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: in.WorkspaceID, Task: in.Task, Harness: in.Harness,
		Mode: in.Mode, Model: in.Model, IdempotencyKey: in.IdempotencyKey}, &res); err != nil {
		return "", nil, err
	}
	out := launched(res.Run, l)
	if res.Replayed {
		out["replayed"] = true
		return fmt.Sprintf("This idempotency_key already launched run %s; nothing new was launched.", res.Run.ID), out, nil
	}
	return fmt.Sprintf("Launched run %s (%s).", res.Run.ID, res.Run.Status), out, nil
}

func swarmLaunched(m protocol.Mission, l links) map[string]any {
	return map[string]any{"swarm_id": m.ID, "phase": m.Phase, "integrator_run_id": m.CurrentIntegratorRunID, "url": l.swarm(m.ID),
		"next": "poll aether_get_swarm with swarm_id"}
}

// recentSwarm is the member's swarm of the same objective, integrator and
// worker choices in the workspace created within duplicateWindow that is
// not cancelled, if any.
func recentSwarm(ctx context.Context, read Call, member string, in protocol.MissionCreateParams, now time.Time) (protocol.Mission, bool, error) {
	var res protocol.MissionListResult
	if err := read(ctx, protocol.MethodMissionList, protocol.MissionListParams{WorkspaceID: in.WorkspaceID, Limit: defaultRows}, &res); err != nil {
		return protocol.Mission{}, false, err
	}
	for _, m := range res.Missions {
		if m.AccountableHumanID == member && m.Objective == in.Objective && sameChoices(m, in) && m.Phase != string(domain.MissionPhaseCancelled) && recent(m.CreatedAt, now) {
			return m, true, nil
		}
	}
	return protocol.Mission{}, false, nil
}

// sameChoices compares agents, modes and models; the server fills in the
// account members.
func sameChoices(m protocol.Mission, p protocol.MissionCreateParams) bool {
	bare := func(c protocol.MissionExecutionChoice) protocol.MissionExecutionChoice {
		c.AccountMemberID = ""
		return c
	}
	if bare(protocol.MissionExecutionChoice(m.Integrator)) != bare(protocol.MissionExecutionChoice(p.Integrator)) || len(m.ExecutionChoices) != len(p.ExecutionChoices) {
		return false
	}
	for _, c := range m.ExecutionChoices {
		if !slices.Contains(p.ExecutionChoices, bare(c)) {
			return false
		}
	}
	return true
}

func launchSwarm(ctx context.Context, c calls, in launchSwarmInput) (string, any, error) {
	mode := cmp.Or(in.Mode, "tui")
	integrator := protocol.MissionExecutionChoice{Harness: in.Harness, Mode: mode, Model: in.Model}
	choices := []protocol.MissionExecutionChoice{integrator}
	for _, w := range in.Workers {
		choice := protocol.MissionExecutionChoice{Harness: w.Harness, Mode: cmp.Or(w.Mode, "tui"), Model: w.Model}
		// The store refuses a choice listed twice.
		if !slices.Contains(choices, choice) {
			choices = append(choices, choice)
		}
	}
	info, err := serverInfo(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	l := links(info.DashboardURL)
	params := protocol.MissionCreateParams{WorkspaceID: in.WorkspaceID, Objective: in.Objective,
		Integrator: protocol.MissionIntegrator(integrator), ExecutionChoices: choices, IdempotencyKey: in.IdempotencyKey}
	if params.IdempotencyKey == "" {
		defer lockLaunches(info.Member.ID)()
		m, dup, err := recentSwarm(ctx, c.read, info.Member.ID, params, time.Now())
		if err != nil {
			return "", nil, err
		}
		if dup {
			out := swarmLaunched(m, l)
			out["duplicate"] = true
			return fmt.Sprintf("You launched this objective at %s; returning swarm %s instead of launching it again. Pass an idempotency_key to launch another.",
				m.CreatedAt, m.ID), out, nil
		}
		// mission.create needs a key; recentSwarm guards retries instead.
		params.IdempotencyKey = uuid.NewString()
	}
	var res protocol.MissionCreateResult
	if err := c.act(ctx, protocol.MethodMissionCreate, params, &res); err != nil {
		return "", nil, err
	}
	m := res.Mission
	return fmt.Sprintf("Launched swarm %s (%s); its integrator is run %s.", m.ID, m.Phase, cmp.Or(m.CurrentIntegratorRunID, "not started yet")), swarmLaunched(m, l), nil
}

func messageRun(ctx context.Context, c calls, in connectorMessageInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var res protocol.RunRoomPostResult
	if err := c.act(ctx, protocol.MethodRunInject, protocol.RunInjectParams{RunID: in.RunID, Message: in.Message, Steer: in.Steer,
		IdempotencyKey: cmp.Or(in.IdempotencyKey, uuid.NewString())}, &res); err != nil {
		return "", nil, err
	}
	m := res.Message
	out := map[string]any{"run_id": in.RunID, "message_id": m.ID, "state": m.State, "outcome": res.Outcome, "url": l.run(in.RunID)}
	summary := fmt.Sprintf("Message to run %s is %s.", in.RunID, m.State)
	if m.State == protocol.RoomMessageQueued && m.DeliverAfter != nil {
		out["deliver_after"] = *m.DeliverAfter
		summary = fmt.Sprintf("Message to run %s is queued: the member in control can approve or deny it, else it is delivered at %s.", in.RunID, *m.DeliverAfter)
	}
	return summary, out, nil
}

func pendingItem(ctx context.Context, read Call, runID, id string) (protocol.AttentionItem, error) {
	items, err := attention(ctx, read, "")
	if err != nil {
		return protocol.AttentionItem{}, err
	}
	for _, it := range items {
		if it.RunID == runID && attentionRowOf(it, "").AnswerID == id {
			return it, nil
		}
	}
	return protocol.AttentionItem{}, &protocol.Error{Code: protocol.CodeNotFound,
		Message: fmt.Sprintf("run %s has nothing pending for you with answer_id %s; it may be answered already", runID, id)}
}

// controlHeld adds the way forward to run.input.respond's refusal while
// another member controls the run.
func controlHeld(err error) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeConflict {
		return err
	}
	var data struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(perr.Data, &data) != nil || data.Reason != protocol.ErrorReasonControlHeld {
		return err
	}
	return &protocol.Error{Code: perr.Code, Data: perr.Data,
		Message: perr.Message + "; ask the member in control to answer, or send your answer to the agent with aether_message_run"}
}

func invalid(format string, args ...any) error {
	return &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

func answer(ctx context.Context, c calls, in answerInput) (string, any, error) {
	it, err := pendingItem(ctx, c.read, in.RunID, in.AnswerID)
	if err != nil {
		return "", nil, err
	}
	key := "connector-answer-" + in.AnswerID
	switch it.Kind {
	case protocol.AttentionKindSwarmQuestion:
		if in.Answer == "" {
			return "", nil, invalid("a swarm question takes answer, the reply text")
		}
		var res protocol.MissionQuestionResult
		if err := c.act(ctx, protocol.MethodMissionQuestionAnswer, protocol.MissionQuestionAnswerParams{QuestionID: in.AnswerID, Answer: in.Answer, IdempotencyKey: key}, &res); err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Answered swarm %s's question.", it.MissionID), map[string]any{"run_id": in.RunID, "question_id": res.Question.ID, "answered": true}, nil
	case protocol.AttentionKindRoomQuestion:
		if in.Answer == "" {
			return "", nil, invalid("a teammate's question takes answer, the reply text")
		}
		var res protocol.RunRoomPostResult
		if err := c.act(ctx, protocol.MethodRunRoomPost, protocol.RunRoomPostParams{WorkspaceID: it.WorkspaceID, RunID: in.RunID,
			Kind: protocol.RoomMessageReply, Body: in.Answer, CorrelationID: in.AnswerID, IdempotencyKey: key}, &res); err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Replied on run %s.", in.RunID), map[string]any{"run_id": in.RunID, "message_id": res.Message.ID, "answered": true}, nil
	case protocol.AttentionKindQuestion:
		if in.OptionID == "" {
			return "", nil, invalid("an agent's question takes option_id, one of the item's options")
		}
		var res protocol.RunInputRespondResult
		if err := c.act(ctx, protocol.MethodRunInputRespond, protocol.RunInputRespondParams{RunID: in.RunID, RequestID: in.AnswerID,
			OptionID: in.OptionID, Values: in.Values}, &res); err != nil {
			return "", nil, controlHeld(err)
		}
		return fmt.Sprintf("Run %s's question: %s.", in.RunID, res.Status), map[string]any{"run_id": in.RunID, "status": res.Status}, nil
	}
	return "", nil, invalid("this item is a %s, which is answered with %s", it.Kind, answerWith(it))
}

// acted reports a run action that succeeded with the run's state after
// it; a failure to read that state is part of the result, never a failed
// call, since the action happened.
func acted(ctx context.Context, read Call, l links, id, did string) (string, any, error) {
	r, err := getRunRecord(ctx, read, id)
	if err != nil {
		return fmt.Sprintf("%s run %s; reading its state failed: %v.", did, id, err), map[string]any{"run_id": id, "state_error": err.Error(), "url": l.run(id)}, nil
	}
	paused := ""
	if r.Paused {
		paused = ", paused"
	}
	return fmt.Sprintf("%s run %s; it is %s%s.", did, r.ID, r.Status, paused),
		map[string]any{"run_id": r.ID, "state": r.Status, "activity": r.Activity, "paused": r.Paused, "review": r.Review, "url": l.run(r.ID)}, nil
}

func runAction(ctx context.Context, c calls, method, id, did string, params any) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	if err := c.act(ctx, method, params, &struct{}{}); err != nil {
		return "", nil, err
	}
	return acted(ctx, c.read, l, id, did)
}

func pauseRun(method, did string) func(context.Context, calls, runIDInput) (string, any, error) {
	return func(ctx context.Context, c calls, in runIDInput) (string, any, error) {
		return runAction(ctx, c, method, in.RunID, did, protocol.RunIDParams{RunID: in.RunID})
	}
}

// markReviewed completes only a run whose reported success awaits review:
// run.review complete closes any live run, which is control's to do.
func markReviewed(ctx context.Context, c calls, in reviewInput) (string, any, error) {
	if in.Action == protocol.RunReviewComplete {
		r, err := getRunRecord(ctx, c.read, in.RunID)
		if err != nil {
			return "", nil, err
		}
		if r.Review != string(domain.ReviewReady) {
			return "", nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
				"run %s has no reported success awaiting review (review %q); to end it, aether_close_run or aether_stop_run, which need the control scope", r.ID, r.Review)}
		}
	}
	return runAction(ctx, c, protocol.MethodRunReview, in.RunID, "Marked reviewed ("+in.Action+")", protocol.RunReviewParams{RunID: in.RunID, Action: in.Action})
}

// decidable refuses a request the connector showed cut, since deciding it
// by id would decide text nobody saw, and one whose action differs from
// what it showed. Neither message quotes the agent's text.
func decidable(in decideInput, l links, shown ...string) error {
	for _, text := range shown {
		if len([]rune(text)) > maxText {
			return &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
				"run %s's request %s is longer than the connector shows; decide it in the Aether dashboard%s", in.RunID, in.AnswerID, at(l.run(in.RunID)))}
		}
	}
	if in.Action != shown[0] {
		return invalid("action is not the pending request's untrusted.text word for word")
	}
	return nil
}

func at(url string) string {
	if url == "" {
		return ""
	}
	return " at " + url
}

func decideApproval(ctx context.Context, c calls, in decideInput) (string, any, error) {
	it, err := pendingItem(ctx, c.read, in.RunID, in.AnswerID)
	if err != nil {
		return "", nil, err
	}
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	allow := in.Decision == "allow"
	switch {
	case it.Kind == protocol.AttentionKindApproval && it.Approval != nil:
		if err := decidable(in, l, it.Approval.Action, it.Approval.Detail); err != nil {
			return "", nil, err
		}
		var res protocol.ApprovalDecideResult
		if err := c.act(ctx, protocol.MethodApprovalDecide, protocol.ApprovalDecideParams{RunID: in.RunID, RequestID: in.AnswerID, Approve: allow}, &res); err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Approval %s on run %s: %s.", in.AnswerID, in.RunID, res.Approval.Decision),
			map[string]any{"run_id": in.RunID, "decision": res.Approval.Decision, "url": l.run(in.RunID), "untrusted": map[string]any{"action": in.Action}}, nil
	case it.Kind == protocol.AttentionKindPermission && it.Request != nil && len(it.Request.Options) > 0:
		if err := decidable(in, l, it.Request.Title); err != nil {
			return "", nil, err
		}
		// allow_always and reject_always would change the agent's policy
		// for later requests, which nobody confirmed.
		want, verb := "reject_once", "Denied"
		if allow {
			want, verb = "allow_once", "Allowed"
		}
		option := ""
		for _, o := range it.Request.Options {
			if o.Kind == want {
				option = o.ID
			}
		}
		if option == "" {
			return "", nil, &protocol.Error{Code: protocol.CodeInvalidState,
				Message: fmt.Sprintf("run %s's request offers no %s option, and the connector never answers for always; decide it in the Aether dashboard%s", in.RunID, want, at(l.run(in.RunID)))}
		}
		var res protocol.RunInputRespondResult
		if err := c.act(ctx, protocol.MethodRunInputRespond, protocol.RunInputRespondParams{RunID: in.RunID, RequestID: in.AnswerID, OptionID: option}, &res); err != nil {
			return "", nil, controlHeld(err)
		}
		return fmt.Sprintf("%s request %s once on run %s: %s.", verb, in.AnswerID, in.RunID, res.Status),
			map[string]any{"run_id": in.RunID, "option": want, "status": res.Status, "url": l.run(in.RunID), "untrusted": map[string]any{"action": in.Action}}, nil
	}
	return "", nil, invalid("this item is a %s, which is answered with %s", it.Kind, answerWith(it))
}

func stopRun(ctx context.Context, c calls, in runIDInput) (string, any, error) {
	return runAction(ctx, c, protocol.MethodRunKill, in.RunID, "Stopped", protocol.RunIDParams{RunID: in.RunID})
}

func closeRun(ctx context.Context, c calls, in closeInput) (string, any, error) {
	return runAction(ctx, c, protocol.MethodRunClose, in.RunID, "Closed ("+in.Outcome+")", protocol.RunCloseParams{RunID: in.RunID, Outcome: in.Outcome})
}

func cancelSwarm(ctx context.Context, c calls, in swarmInput) (string, any, error) {
	var res protocol.MissionCancelResult
	if err := c.act(ctx, protocol.MethodMissionCancel, protocol.MissionCancelParams{MissionID: in.SwarmID, IdempotencyKey: "connector-cancel-" + in.SwarmID}, &res); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Swarm %s is %s.", res.Mission.ID, res.Mission.Phase), map[string]any{"swarm_id": res.Mission.ID, "phase": res.Mission.Phase}, nil
}
