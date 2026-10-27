package ops

import (
	"cmp"
	"context"
	"crypto/sha256"
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
	Harness        string `json:"harness" jsonschema:"an agent from aether_list_workspaces"`
	Mode           string `json:"mode,omitempty" jsonschema:"acp is enhanced; default acp if installed"`
	Model          string `json:"model,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type swarmWorker struct {
	Harness string `json:"harness"`
	Mode    string `json:"mode,omitempty"`
	Model   string `json:"model,omitempty"`
}

type launchSwarmInput struct {
	WorkspaceID    string        `json:"workspace_id"`
	Objective      string        `json:"objective" jsonschema:"the goal, as a complete brief"`
	Harness        string        `json:"harness" jsonschema:"the integrator's agent"`
	Mode           string        `json:"mode,omitempty"`
	Model          string        `json:"model,omitempty"`
	Workers        []swarmWorker `json:"workers,omitempty" jsonschema:"default the integrator's agent"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
}

type connectorMessageInput struct {
	RunID          string `json:"run_id"`
	Message        string `json:"message"`
	Steer          bool   `json:"steer,omitempty" jsonschema:"join an enhanced run's current turn"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type answerInput struct {
	RunID    string         `json:"run_id"`
	AnswerID string         `json:"answer_id" jsonschema:"from aether_needs_attention"`
	Answer   string         `json:"answer,omitempty"`
	OptionID string         `json:"option_id,omitempty"`
	Values   map[string]any `json:"values,omitempty" jsonschema:"form values"`
	// IdempotencyKey repeats a swarm or teammate answer safely; an agent's
	// question takes one answer, so a repeat finds nothing pending.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
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

type stopInput struct {
	RunID   string `json:"run_id"`
	Outcome string `json:"outcome,omitempty" jsonschema:"close the run as this"`
}

const idempotencyRule = " Reuse idempotency_key on a retry; "

var writeOps = []Op{
	opOf("aether_launch_run", "Launch run", ScopeWrite, false, nextWorkspaces,
		"Use this when the user asks to start an agent on a task, one call per task; then poll aether_get_run."+
			idempotencyRule+"without one, a repeat within 60s returns the first run.", launchRun).
		enum("mode", "tui", "headless", "acp").hints(false, true).
		offer(TierConfirmed).calling(protocol.MethodServerInfo, protocol.MethodRunList, protocol.MethodRunLaunch),
	opOf("aether_launch_swarm", "Launch swarm", ScopeWrite, false, nextWorkspaces,
		"Use this when the user wants one goal split across agents: an integrator plans tasks for workers; then poll aether_get_swarm."+
			idempotencyRule+"without one, a repeat within 60s returns the first swarm.", launchSwarm).
		enum("mode", "tui", "acp").enum("workers.mode", "tui", "headless", "acp").hints(false, true).
		calling(protocol.MethodAgentList, protocol.MethodServerInfo, protocol.MethodMissionList, protocol.MethodMissionCreate),
	opOf("aether_message_run", "Message run", ScopeWrite, false, nextRuns,
		"Use this when the user wants to tell a run's agent something, which cannot be taken back; it waits up to 45s for approval if someone else controls the run."+
			idempotencyRule+"without one, a repeat within 60s is sent once.", messageRun).hints(true, true).
		offer(TierConfirmed).calling(protocol.MethodServerInfo, protocol.MethodRunInject),
	opOf("aether_answer", "Answer question", ScopeWrite, true, nextAttention,
		"Use this when an aether_needs_attention item's answer_with is aether_answer: option_id (and values) for an agent's question, else answer.", answer).hints(true, true).
		offer(TierConfirmed).calling(protocol.MethodAttentionList, protocol.MethodMissionQuestionAnswer, protocol.MethodRunRoomPost, protocol.MethodRunInputRespond, protocol.MethodRunGet,
		protocol.MethodMissionShow, protocol.MethodRunRoomList),
	opOf("aether_pause_run", "Pause run", ScopeWrite, true, nextRuns,
		"Use this when the user wants a run paused; its container freezes until aether_resume_run.", pauseRun(protocol.MethodRunPause, "Paused")).
		offer(TierReversible).calling(protocol.MethodServerInfo, protocol.MethodRunPause, protocol.MethodRunGet),
	opOf("aether_resume_run", "Resume run", ScopeWrite, true, nextRuns,
		"Use this when the user wants a paused run to continue.", pauseRun(protocol.MethodRunResume, "Resumed")).hints(false, true).
		offer(TierReversible).calling(protocol.MethodServerInfo, protocol.MethodRunResume, protocol.MethodRunGet),
	opOf("aether_mark_reviewed", "Mark run reviewed", ScopeWrite, true, nextRuns,
		"Use this when the user has checked a run's reported success: complete accepts it and closes the run, later puts it off.", markReviewed).
		enum("action", protocol.RunReviewComplete, protocol.RunReviewLater).hints(true, false).
		calling(protocol.MethodRunGet, protocol.MethodServerInfo, protocol.MethodRunReview),
}

var controlOps = []Op{
	opOf("aether_decide_approval", "Decide approval", ScopeControl, true, nextAttention,
		"Use this when the user allows or denies a permission item from aether_needs_attention, once; an allowed action cannot be taken back.", decideApproval).
		enum("decision", "allow", "deny").hints(true, true).
		offer(TierConfirmed).calling(protocol.MethodAttentionList, protocol.MethodServerInfo, protocol.MethodRunInputRespond),
	opOf("aether_stop_run", "Stop or close run", ScopeControl, true, nextRuns,
		"Use this when the user wants a run's agent stopped now, or, with outcome, the run closed as merged or abandoned.", stopRun).
		enum("outcome", "merged", "abandoned").hints(true, false).
		offer(TierConfirmed).calling(protocol.MethodServerInfo, protocol.MethodRunKill, protocol.MethodRunClose, protocol.MethodRunGet),
	opOf("aether_cancel_swarm", "Cancel swarm", ScopeControl, true, nextSwarms,
		"Use this when the user wants a swarm ended; it stops its integrator and workers.", cancelSwarm).hints(true, false).
		calling(protocol.MethodMissionCancel),
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
// it from the duplicate check until its launch is in flight, so two
// overlapping retries through this server cannot both launch.
var launchLocks sync.Map

func lockLaunches(member string) func() {
	mu, _ := launchLocks.LoadOrStore(member, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

// inflight maps a member's keyless launch still in flight, by method and
// params, to the idempotency key derived for it; an identical keyless call
// replays that key and gets the same run or swarm.
var inflight sync.Map

// keyless gives a launch with no idempotency key one: an identical
// launch's still in flight (dup is true), else a new one, unless guard,
// run under the member's lock, finds a recent identical launch (found is
// true). done ends the in-flight entry when the launch call returns with
// err, or duplicateWindow later when err leaves unknown whether the launch
// happened, so a keyless retry meanwhile replays the key.
func keyless(member, method string, params any, guard func() (bool, error)) (key string, dup, found bool, done func(error), err error) {
	defer lockLaunches(member)()
	b, err := json.Marshal(params)
	if err != nil {
		return "", false, false, nil, fmt.Errorf("fingerprint launch: %w", err)
	}
	fingerprint := member + "\x00" + method + "\x00" + string(b)
	if k, ok := inflight.Load(fingerprint); ok {
		return k.(string), true, false, func(error) {}, nil
	}
	if found, err = guard(); err != nil || found {
		return "", false, found, nil, err
	}
	key = uuid.NewString()
	inflight.Store(fingerprint, key)
	return key, false, false, func(err error) {
		forget := func() { inflight.CompareAndDelete(fingerprint, key) }
		if uncertain(err) {
			time.AfterFunc(duplicateWindow, forget)
			return
		}
		forget()
	}, nil
}

// launchWait is how long a launch tool waits for the launch call before
// answering with what it started; provisioning can take minutes.
var launchWait = toolTimeout - 10*time.Second

// replayWait bounds the replay that recovers what a launch started.
const replayWait = 5 * time.Second

// launch calls method, a launch whose params carry an idempotency key, on
// a context the tool's deadline and the client's disconnect do not cancel,
// since cancelling it fails the run it is provisioning. Past launchWait it
// replays the key, which the server answers as soon as the run or swarm
// exists; finished is false then, err is the replay's, and res is empty if
// that replay failed or is not answered in time either. done runs when the
// launch call returns. A surface with no budget waits for the launch, since
// its member may still be deciding whether to approve it.
func launch[R any](ctx context.Context, c calls, method string, params any, done func(error)) (res R, finished bool, err error) {
	act := c.act
	if c.surface.budget() == 0 {
		err = act(ctx, method, params, &res)
		done(err)
		return res, true, err
	}
	detached := context.WithoutCancel(ctx)
	first := make(chan error, 1)
	var launched R
	go func() {
		err := act(detached, method, params, &launched)
		done(err)
		first <- err
	}()
	select {
	case err := <-first:
		return launched, true, err
	case <-ctx.Done():
	case <-time.After(launchWait):
	}
	replay := make(chan error, 1)
	var replayed R
	go func() { replay <- act(context.WithValue(detached, replayKey{}, true), method, params, &replayed) }()
	select {
	case err := <-replay:
		if err != nil {
			return res, false, err
		}
		return replayed, false, nil
	case <-time.After(replayWait):
		return res, false, nil
	}
}

// starting answers a launch the replay did not recover, with replayErr
// when the replay failed.
func starting(tool, key string, replayErr error) (string, any, error) {
	text := fmt.Sprintf("The launch is still starting; call %s again with the same arguments and idempotency_key %s to get it.", tool, key)
	out := map[string]any{"state": "starting", "idempotency_key": key, "write": writePending,
		"next": "call " + tool + " again with the same arguments and idempotency_key"}
	if replayErr != nil {
		text += " Looking it up failed: " + replayErr.Error()
		out["error"] = replayErr.Error()
	}
	return text, out, nil
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
	params := protocol.RunLaunchParams{WorkspaceID: in.WorkspaceID, Task: in.Task, Harness: in.Harness, Mode: in.Mode, Model: in.Model,
		IdempotencyKey: in.IdempotencyKey}
	dup, done := false, func(error) {}
	if params.IdempotencyKey == "" {
		var prior protocol.Run
		var found bool
		params.IdempotencyKey, dup, found, done, err = keyless(info.Member.ID, protocol.MethodRunLaunch, params, func() (ok bool, err error) {
			prior, ok, err = recentLaunch(ctx, c.read, in, time.Now())
			return ok, err
		})
		if err != nil {
			return "", nil, err
		}
		if found {
			out := launched(prior, l)
			out["duplicate"] = true
			return fmt.Sprintf("You launched this task with %s at %s; returning run %s instead of launching it again. Pass an idempotency_key to launch another.",
				prior.Harness, prior.CreatedAt, prior.ID), out, nil
		}
	}
	res, finished, err := launch[protocol.RunLaunchResult](ctx, c, protocol.MethodRunLaunch, params, done)
	if err != nil && finished {
		return "", nil, retryable(err, params.IdempotencyKey)
	}
	if res.Run.ID == "" {
		return starting("aether_launch_run", params.IdempotencyKey, err)
	}
	out := launched(res.Run, l)
	out["idempotency_key"] = params.IdempotencyKey
	switch {
	case dup:
		out["duplicate"] = true
		return fmt.Sprintf("You are already launching this task; returning run %s instead of launching it again. Pass an idempotency_key to launch another.", res.Run.ID), out, nil
	case !finished:
		out["state"], out["write"] = "starting", writePending
		return fmt.Sprintf("Run %s is starting.", res.Run.ID), out, nil
	case res.Replayed:
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
	var listed protocol.AgentListResult
	if in.Mode == "" || slices.ContainsFunc(in.Workers, func(w swarmWorker) bool { return w.Mode == "" }) {
		if err := c.read(ctx, protocol.MethodAgentList, nil, &listed); err != nil {
			return "", nil, fmt.Errorf("list agents to choose their modes (or pass every mode): %w", err)
		}
	}
	mode := func(agent, given string) (string, error) {
		if given != "" {
			return given, nil
		}
		return protocol.SwarmMode(listed.Agents, agent)
	}
	integratorMode, err := mode(in.Harness, in.Mode)
	if err != nil {
		return "", nil, err
	}
	integrator := protocol.MissionExecutionChoice{Harness: in.Harness, Mode: integratorMode, Model: in.Model}
	choices := []protocol.MissionExecutionChoice{integrator}
	for _, w := range in.Workers {
		workerMode, modeErr := mode(w.Harness, w.Mode)
		if modeErr != nil {
			return "", nil, modeErr
		}
		choice := protocol.MissionExecutionChoice{Harness: w.Harness, Mode: workerMode, Model: w.Model}
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
	dup, done := false, func(error) {}
	if params.IdempotencyKey == "" {
		var prior protocol.Mission
		var found bool
		params.IdempotencyKey, dup, found, done, err = keyless(info.Member.ID, protocol.MethodMissionCreate, params, func() (ok bool, err error) {
			prior, ok, err = recentSwarm(ctx, c.read, info.Member.ID, params, time.Now())
			return ok, err
		})
		if err != nil {
			return "", nil, err
		}
		if found {
			out := swarmLaunched(prior, l)
			out["duplicate"] = true
			return fmt.Sprintf("You launched this objective at %s; returning swarm %s instead of launching it again. Pass an idempotency_key to launch another.",
				prior.CreatedAt, prior.ID), out, nil
		}
	}
	res, finished, err := launch[protocol.MissionCreateResult](ctx, c, protocol.MethodMissionCreate, params, done)
	if err != nil && finished {
		return "", nil, retryable(err, params.IdempotencyKey)
	}
	m := res.Mission
	if m.ID == "" {
		return starting("aether_launch_swarm", params.IdempotencyKey, err)
	}
	out := swarmLaunched(m, l)
	out["idempotency_key"] = params.IdempotencyKey
	switch {
	case dup:
		out["duplicate"] = true
		return fmt.Sprintf("You are already launching this objective; returning swarm %s instead of launching it again. Pass an idempotency_key to launch another.", m.ID), out, nil
	case !finished:
		out["state"], out["write"] = "starting", writePending
		return fmt.Sprintf("Swarm %s is starting; its integrator is run %s.", m.ID, cmp.Or(m.CurrentIntegratorRunID, "not started yet")), out, nil
	}
	return fmt.Sprintf("Launched swarm %s (%s); its integrator is run %s.", m.ID, m.Phase, cmp.Or(m.CurrentIntegratorRunID, "not started yet")), out, nil
}

// sentKeys remembers the key of each keyless message by member, run, steer
// and text while it is in flight and for duplicateWindow after.
var sentKeys = struct {
	sync.Mutex
	keys map[string]*sentKey
}{keys: map[string]*sentKey{}}

type sentKey struct {
	key      string
	at       time.Time
	inflight int
	sent     bool
}

// messageKey is the key of a keyless message: an identical message's still
// in flight or from within duplicateWindow, which the server delivers once,
// else a new one. dup is true when an identical message is in flight or was
// sent. done ends the call with whether it succeeded and restarts the
// window, since an assistant call waits for the member's approval first.
func messageKey(member string, p protocol.RunInjectParams, now time.Time) (key string, dup bool, done func(sent bool)) {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%t\x00%s", member, p.RunID, p.Steer, p.Message))
	fingerprint := string(sum[:])
	sentKeys.Lock()
	defer sentKeys.Unlock()
	for f, k := range sentKeys.keys {
		if k.inflight == 0 && now.Sub(k.at) >= duplicateWindow {
			delete(sentKeys.keys, f)
		}
	}
	k, ok := sentKeys.keys[fingerprint]
	if !ok {
		k = &sentKey{key: uuid.NewString()}
		sentKeys.keys[fingerprint] = k
	}
	dup = k.inflight > 0 || k.sent
	k.inflight++
	return k.key, dup, func(sent bool) {
		sentKeys.Lock()
		defer sentKeys.Unlock()
		k.inflight--
		k.at = time.Now()
		k.sent = k.sent || sent
	}
}

func messageRun(ctx context.Context, c calls, in connectorMessageInput) (string, any, error) {
	info, err := serverInfo(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	l := links(info.DashboardURL)
	params := protocol.RunInjectParams{RunID: in.RunID, Message: in.Message, Steer: in.Steer, IdempotencyKey: in.IdempotencyKey}
	dup, done := false, func(bool) {}
	if params.IdempotencyKey == "" {
		params.IdempotencyKey, dup, done = messageKey(info.Member.ID, params, time.Now())
	}
	var res protocol.RunRoomPostResult
	err = c.act(ctx, protocol.MethodRunInject, params, &res)
	done(err == nil)
	if err != nil {
		return "", nil, retryable(err, params.IdempotencyKey)
	}
	m := res.Message
	out := map[string]any{"run_id": in.RunID, "state": m.State, "outcome": res.Outcome, "url": l.run(in.RunID), "idempotency_key": params.IdempotencyKey}
	summary := fmt.Sprintf("Message to run %s is %s.", in.RunID, m.State)
	if m.State == protocol.RoomMessageQueued && m.DeliverAfter != nil {
		out["deliver_after"], out["write"] = *m.DeliverAfter, writePending
		summary = fmt.Sprintf("Message to run %s is queued: the member in control can approve or deny it, else it is delivered at %s.", in.RunID, *m.DeliverAfter)
	}
	if dup {
		out["duplicate"] = true
		summary = fmt.Sprintf("You sent this message to run %s within the last 60s; returning it (%s) instead of sending it again. Pass a new idempotency_key to send the same text again.", in.RunID, m.State)
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
		Message: fmt.Sprintf("run %s has nothing pending for you with that answer_id; it may be answered already", runID)}
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
	var perr *protocol.Error
	if errors.As(err, &perr) && perr.Code == protocol.CodeNotFound && in.IdempotencyKey != "" && in.Answer != "" {
		return answerAgain(ctx, c, in, err)
	}
	if err != nil {
		return "", nil, err
	}
	key := cmp.Or(in.IdempotencyKey, "connector-answer-"+in.AnswerID)
	switch it.Kind {
	case protocol.AttentionKindSwarmQuestion:
		if in.Answer == "" {
			return "", nil, invalid("a swarm question takes answer, the reply text")
		}
		var res protocol.MissionQuestionResult
		if err := c.act(ctx, protocol.MethodMissionQuestionAnswer, protocol.MissionQuestionAnswerParams{QuestionID: in.AnswerID, Answer: in.Answer, IdempotencyKey: key}, &res); err != nil {
			return "", nil, retryable(err, key)
		}
		return fmt.Sprintf("Answered swarm %s's question.", it.MissionID), map[string]any{"run_id": in.RunID, "answered": true, "idempotency_key": key}, nil
	case protocol.AttentionKindRoomQuestion:
		if in.Answer == "" {
			return "", nil, invalid("a teammate's question takes answer, the reply text")
		}
		var res protocol.RunRoomPostResult
		if err := c.act(ctx, protocol.MethodRunRoomPost, protocol.RunRoomPostParams{WorkspaceID: it.WorkspaceID, RunID: in.RunID,
			Kind: protocol.RoomMessageReply, Body: in.Answer, CorrelationID: in.AnswerID, IdempotencyKey: key}, &res); err != nil {
			return "", nil, retryable(err, key)
		}
		return fmt.Sprintf("Replied on run %s.", in.RunID), map[string]any{"run_id": in.RunID, "answered": true, "idempotency_key": key}, nil
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

// answerAgain repeats a keyed swarm or teammate answer whose question is
// no longer pending, so a retry after a lost response gets the saved answer
// back: the server returns what the key already did. It answers only a
// swarm question by that id, or a teammate question on the run that the id
// names or the key already replied to; anything else fails with notPending.
func answerAgain(ctx context.Context, c calls, in answerInput, notPending error) (string, any, error) {
	key := in.IdempotencyKey
	var shown protocol.MissionShowResult
	err := c.read(ctx, protocol.MethodMissionShow, protocol.MissionShowParams{QuestionID: in.AnswerID}, &shown)
	var perr *protocol.Error
	switch {
	case err == nil:
		var q protocol.MissionQuestionResult
		if actErr := c.act(ctx, protocol.MethodMissionQuestionAnswer, protocol.MissionQuestionAnswerParams{QuestionID: in.AnswerID, Answer: in.Answer, IdempotencyKey: key}, &q); actErr != nil {
			return "", nil, retryable(actErr, key)
		}
		return fmt.Sprintf("Answered swarm %s's question.", shown.Mission.ID), map[string]any{"run_id": in.RunID, "answered": true, "idempotency_key": key}, nil
	case !errors.As(err, &perr) || perr.Code != protocol.CodeNotFound:
		return "", nil, err
	}
	r, err := getRunRecord(ctx, c.read, in.RunID)
	if err != nil {
		return "", nil, notPending
	}
	asked, err := roomQuestion(ctx, c.read, r, in.AnswerID, key)
	if err != nil {
		return "", nil, err
	}
	if !asked {
		return "", nil, notPending
	}
	var res protocol.RunRoomPostResult
	if err := c.act(ctx, protocol.MethodRunRoomPost, protocol.RunRoomPostParams{WorkspaceID: r.WorkspaceID, RunID: r.ID,
		Kind: protocol.RoomMessageReply, Body: in.Answer, CorrelationID: in.AnswerID, IdempotencyKey: key}, &res); err != nil {
		return "", nil, retryable(err, key)
	}
	return fmt.Sprintf("Replied on run %s.", r.ID), map[string]any{"run_id": r.ID, "answered": true, "idempotency_key": key}, nil
}

// roomPages bounds how many pages of a run's room roomQuestion reads.
const roomPages = 5

// roomQuestion is whether run r's room, newest first, holds question id or a
// reply to it under key.
func roomQuestion(ctx context.Context, read Call, r protocol.Run, id, key string) (bool, error) {
	before := ""
	for range roomPages {
		var page protocol.RoomMessageListResult
		if err := read(ctx, protocol.MethodRunRoomList, protocol.RunRoomListParams{WorkspaceID: r.WorkspaceID, RunID: r.ID, Before: before,
			Limit: protocol.CollaborationMaxPageSize}, &page); err != nil {
			return false, err
		}
		if slices.ContainsFunc(page.Messages, func(m protocol.RoomMessage) bool {
			return (m.ID == id && m.Kind == protocol.RoomMessageQuestion) || (m.IdempotencyKey == key && m.CorrelationID == id)
		}) {
			return true, nil
		}
		if page.NextBefore == "" {
			break
		}
		before = page.NextBefore
	}
	return false, nil
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
				"run %s has no reported success awaiting review (review %q); to end it, aether_stop_run, which needs the control scope", r.ID, r.Review)}
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
				"run %s's request is longer than the connector shows; decide it in the Aether dashboard%s", in.RunID, at(l.run(in.RunID)))}
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
		return fmt.Sprintf("%s the request once on run %s: %s.", verb, in.RunID, res.Status),
			map[string]any{"run_id": in.RunID, "option": want, "status": res.Status, "url": l.run(in.RunID), "untrusted": map[string]any{"action": in.Action}}, nil
	}
	return "", nil, invalid("this item is a %s, which is answered with %s", it.Kind, answerWith(it))
}

func stopRun(ctx context.Context, c calls, in stopInput) (string, any, error) {
	if in.Outcome == "" {
		return runAction(ctx, c, protocol.MethodRunKill, in.RunID, "Stopped", protocol.RunIDParams{RunID: in.RunID})
	}
	return runAction(ctx, c, protocol.MethodRunClose, in.RunID, "Closed ("+in.Outcome+")", protocol.RunCloseParams{RunID: in.RunID, Outcome: in.Outcome})
}

func cancelSwarm(ctx context.Context, c calls, in swarmInput) (string, any, error) {
	var res protocol.MissionCancelResult
	if err := c.act(ctx, protocol.MethodMissionCancel, protocol.MissionCancelParams{MissionID: in.SwarmID, IdempotencyKey: "connector-cancel-" + in.SwarmID}, &res); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Swarm %s is %s.", res.Mission.ID, res.Mission.Phase), map[string]any{"swarm_id": res.Mission.ID, "phase": res.Mission.Phase}, nil
}
