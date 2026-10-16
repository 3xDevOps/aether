package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	// acpConnectTimeout bounds starting the adapter and opening its session:
	// a cold npm adapter takes seconds, a hung one must not hold a launch.
	acpConnectTimeout = time.Minute
	acpStopGrace      = 3 * time.Second
	acpStderrTail     = 4 << 10
	// acpReportWait bounds how long a report waits for its run to leave
	// provisioning: the adapter can answer before the launch marks the run
	// running.
	acpReportWait = 30 * time.Second
	// acpSteerTimeout bounds a delivery, which runs under the run's
	// admission lock and may wait on the agent's answer to a steer.
	acpSteerTimeout = 15 * time.Second
	oneShotResume   = "Aether's server restarted and interrupted your previous turn. Continue the task where you stopped."
)

// Claude Code stored its title within a second of the prompt in every
// traced session.
var acpTitleLookupDelay = 3 * time.Second

// ErrACPNotRunning rejects input for an enhanced run with no live session.
var ErrACPNotRunning = errors.New("scheduler: the enhanced session is not running")

// acpDriver hosts an enhanced run: the container's primary PTY is a login
// shell, and the agent's ACP server runs beside it as a managed exec whose
// stdio the acphost session owns. The adapter never outlives that pipe, so
// every reattach stops any earlier exec before starting a fresh one.
type acpDriver struct {
	s *Scheduler

	mu      sync.Mutex
	closed  bool
	runs    map[domain.RunID]*acpRun
	ops     map[domain.RunID]*opLock
	waiters map[domain.RunID]*waiter
}

// opLock and waiter entries live only while someone holds or waits on them.
type opLock struct {
	mu   sync.Mutex
	refs int
}

type waiter struct {
	ch   chan struct{}
	refs int
}

type acpRun struct {
	session  *acphost.Session
	exec     runtime.ManagedExec
	stderr   *tailBuffer
	err      error
	stopping bool
	ended    bool
	// untried marks a first task sent but not yet accepted, so a refusal
	// fails a worker whichever of startOneShot or the turn end reports it.
	untried bool
	// instructions are the run instructions the first message or mail prompt
	// a taskless session accepts carries, for an agent with no system prompt
	// route.
	instructions string
}

func newACPDriver(s *Scheduler) *acpDriver {
	return &acpDriver{
		s:       s,
		runs:    make(map[domain.RunID]*acpRun),
		ops:     make(map[domain.RunID]*opLock),
		waiters: make(map[domain.RunID]*waiter),
	}
}

// lockOp takes the run's op lock and returns its unlock.
func (d *acpDriver) lockOp(run domain.RunID) func() {
	d.mu.Lock()
	l := d.ops[run]
	if l == nil {
		l = new(opLock)
		d.ops[run] = l
	}
	l.refs++
	d.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		d.mu.Lock()
		defer d.mu.Unlock()
		if l.refs--; l.refs == 0 {
			delete(d.ops, run)
		}
	}
}

func (d *acpDriver) Start(ctx context.Context, entry *supervised, att runtime.Attachment) error {
	if err := d.s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(entry.runID), att); err != nil {
		return err
	}
	if !d.continuationOwed(entry) {
		d.connect(ctx, entry, openNew)
		return nil
	}
	// The move that started this container reports a failed restore as its
	// own error, so it is recorded here rather than reported.
	if err := d.openRestored(ctx, entry, openContinue); err != nil {
		d.setRun(entry.runID, &acpRun{err: err})
		d.notice(entry.runID, "error", "Enhanced session failed", err.Error())
	}
	return nil
}

func (d *acpDriver) continuationOwed(entry *supervised) bool {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	return entry.accountSwap != nil && entry.accountSwap.Continuation != ""
}

func (d *acpDriver) sendContinuation(entry *supervised) {
	d.s.mu.Lock()
	swap := entry.accountSwap
	d.s.mu.Unlock()
	if swap == nil || swap.Continuation == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), acpConnectTimeout)
	defer cancel()
	sess, err := d.live(entry.runID)
	if err == nil {
		_, err = sess.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(swap.Continuation)}, false, nil)
	}
	d.s.clearAccountSwap(entry, swap)
	switch {
	case err == nil || usageLimited(err):
	case d.oneShot(entry):
		d.endOneShot(entry, false, false, agentstatus.Report{State: agentstatus.Idle, Reason: "send the continuation to the agent: " + err.Error()})
	default:
		slog.Warn("scheduler: send the continuation to the agent", "run", entry.runID, "error", err)
	}
}

// usageLimited reports a prompt refused on the account's usage limit, which
// the session's turn-end callback hands to a move: ending a background run
// for it here would race that move.
func usageLimited(err error) bool {
	_, ok := acphost.UsageLimit(err)
	return ok
}

func (d *acpDriver) Resume(ctx context.Context, entry *supervised, att runtime.Attachment) error {
	if err := d.s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(entry.runID), att); err != nil {
		return err
	}
	if !d.continuationOwed(entry) {
		d.connect(ctx, entry, openRestore)
		return nil
	}
	d.connect(ctx, entry, openContinue)
	d.sendContinuation(entry)
	return nil
}

func (d *acpDriver) Stop(ctx context.Context, run domain.RunID) error {
	return errors.Join(d.stopAdapter(ctx, run), d.s.cfg.PTY.StopSession(ctx, ptyhost.RunSession(run)))
}

func (d *acpDriver) LastActivity(run domain.RunID) time.Time {
	if sess := d.session(run); sess != nil {
		return sess.State().LastActivity
	}
	return time.Time{}
}

func (d *acpDriver) Deliver(ctx context.Context, run *domain.Run, _ *domain.Member, prompt domain.AgentPrompt, steer bool, delivered func(error)) (string, error) {
	sess, err := d.live(run.ID)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ptyhost.ErrNoSession, err)
	}
	ctx, cancel := context.WithTimeout(ctx, acpSteerTimeout)
	defer cancel()
	blocks := make([]acp.ContentBlock, 0, len(prompt.Attachments)+1)
	if prompt.Text != "" || len(prompt.Attachments) == 0 {
		blocks = append(blocks, acp.TextBlock(prompt.Text))
	}
	if !sess.Info().PromptImages && slices.ContainsFunc(prompt.Attachments, func(reference string) bool {
		_, file := attachedFileName(reference)
		return !file
	}) {
		return "", acphost.ErrUnsupportedImage
	}
	for index, reference := range prompt.Attachments {
		if _, file := attachedFileName(reference); file {
			size, mimeType, statErr := d.s.StatAttachedFile(ctx, run.ID, reference)
			if statErr != nil {
				return "", fmt.Errorf("%w: scheduler: stat prompt file: %w", domain.ErrPromptNotSent, statErr)
			}
			block := acp.ResourceLinkBlock(path.Base(reference), (&url.URL{Scheme: "file", Path: reference}).String())
			n := int(size)
			block.ResourceLink.MimeType, block.ResourceLink.Size = &mimeType, &n
			blocks = append(blocks, block)
			continue
		}
		data, mimeType, readErr := d.s.ReadImage(ctx, run.ID, reference)
		if readErr != nil {
			return "", fmt.Errorf("%w: scheduler: read prompt image: %w", domain.ErrPromptNotSent, readErr)
		}
		uri := "aether://room/" + url.PathEscape(prompt.MessageID) + "/" + strconv.Itoa(index)
		block := acp.ImageBlock(base64.StdEncoding.EncodeToString(data), mimeType)
		block.Image.Uri = &uri
		blocks = append(blocks, block)
	}
	untitled := run.Task == "" && run.Title == ""
	sent := time.Now().UTC()
	instructions, settle := d.claimInstructions(run.ID, sess)
	if instructions != "" {
		blocks = append(blocks, acp.TextBlock("\n\n"+instructions))
	}
	receipt, err := sess.Prompt(ctx, blocks, steer, func(queued bool, taken error) {
		settle(taken)
		if taken == nil {
			if answerErr := d.s.answerReached(run.ID, sent); answerErr != nil {
				slog.Warn("scheduler: a member's message reached the agent", "run", run.ID, "error", answerErr)
			}
		}
		if untitled && taken == nil {
			d.s.setProvisionalRunTitle(run.ID, prompt.Text)
		}
		if queued && delivered != nil {
			delivered(taken)
		}
	})
	if err != nil && ctx.Err() == nil {
		// No turn took the prompt; one awaiting acceptance settles through
		// the callback.
		settle(err)
	}
	if errors.Is(err, acphost.ErrClosed) {
		err = fmt.Errorf("%w: %w", ptyhost.ErrSessionEnded, err)
	}
	return receipt.Outcome, err
}

// claimInstructions takes the run instructions sess still owes for one
// prompt. settle gives them back when that prompt never reached the agent;
// only its first call counts.
func (d *acpDriver) claimInstructions(run domain.RunID, sess *acphost.Session) (instructions string, settle func(error)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[run]
	if r == nil || r.session != sess || r.instructions == "" {
		return "", func(error) {}
	}
	instructions, r.instructions = r.instructions, ""
	var once sync.Once
	return instructions, func(err error) {
		once.Do(func() {
			if err == nil {
				return
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.runs[run] == r && r.instructions == "" {
				r.instructions = instructions
			}
		})
	}
}

func (d *acpDriver) session(run domain.RunID) *acphost.Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.runs[run]; r != nil {
		return r.session
	}
	return nil
}

func (d *acpDriver) live(run domain.RunID) (*acphost.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[run]
	switch {
	case r != nil && r.session != nil && !r.ended:
		return r.session, nil
	case r != nil && r.err != nil:
		return nil, fmt.Errorf("%w: %w", ErrACPNotRunning, r.err)
	}
	return nil, ErrACPNotRunning
}

// openError is the failure the run's last open recorded, nil when it opened.
// A background run that then ended its session on purpose has none: its own
// ending carries the reason.
func (d *acpDriver) openError(run domain.RunID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.runs[run]; r != nil && r.session == nil && !r.ended {
		return r.err
	}
	return nil
}

// started returns the live session, or else a channel closed when the next
// one starts and the release for it, both read under one lock.
func (d *acpDriver) started(run domain.RunID) (*acphost.Session, <-chan struct{}, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.runs[run]; r != nil && r.session != nil {
		return r.session, nil, func() {}
	}
	w := d.waiters[run]
	if w == nil {
		w = &waiter{ch: make(chan struct{})}
		d.waiters[run] = w
	}
	w.refs++
	var once sync.Once
	return nil, w.ch, func() {
		once.Do(func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			if w.refs--; w.refs == 0 && d.waiters[run] == w {
				delete(d.waiters, run)
			}
		})
	}
}

type acpOpen int

const (
	openNew acpOpen = iota
	// openRestore starts a new session when the stored one cannot be restored.
	openRestore
	openSwitch
	// openContinue restores the session in a container that replaced the
	// run's, on another account; the move sends it its continuation.
	openContinue
)

func (d *acpDriver) connect(ctx context.Context, entry *supervised, how acpOpen) {
	defer d.lockOp(entry.runID)()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), acpConnectTimeout)
	defer cancel()
	if err := d.open(ctx, entry, how); err != nil {
		d.fail(entry, err)
	}
}

// openRestored restores the run's stored session, how being openSwitch or
// openContinue, and returns the failure instead of recording it.
func (d *acpDriver) openRestored(ctx context.Context, entry *supervised, how acpOpen) error {
	defer d.lockOp(entry.runID)()
	ctx, cancel := context.WithTimeout(ctx, acpConnectTimeout)
	defer cancel()
	return d.open(ctx, entry, how)
}

// open runs under the run's op lock.
func (d *acpDriver) open(ctx context.Context, entry *supervised, how acpOpen) error {
	if err := d.stopAdapterLocked(ctx, entry.runID); err != nil {
		slog.Warn("scheduler: stop the previous ACP server", "run", entry.runID, "error", err)
	}
	d.stopStaleExec(ctx, entry)

	d.s.mu.Lock()
	paused, cid, coordDir, cachedSession, task := entry.paused, entry.containerID, entry.coordDir, entry.agentSessionID, entry.task
	oneShot := entry.launchMode == domain.LaunchHeadless
	marked := entry.usageLimit != ""
	d.s.mu.Unlock()
	if paused {
		// Docker cannot exec into a frozen container; Resume connects.
		d.setRun(entry.runID, &acpRun{err: errors.New("the container is paused")})
		return nil
	}
	run, err := d.s.cfg.Store.GetRun(ctx, entry.runID)
	if err != nil {
		return fmt.Errorf("load run: %w", err)
	}
	profile, argv, err := d.s.launchProfile(ctx, run.MemberID, run.AccountMember(), run.Harness)
	if err != nil {
		return err
	}
	adapter := argv[domain.LaunchACP]
	if len(adapter) == 0 {
		return fmt.Errorf("agent %q has no ACP command", run.Harness)
	}
	managed, ok := d.s.cfg.Runtime.(runtime.ManagedExecRuntime)
	if !ok {
		return fmt.Errorf("%w: the runtime cannot run a managed exec", runtime.ErrExecUnavailable)
	}
	fresh := how == openNew
	sessionID, mode := "", profile.ACPMode
	if !fresh {
		sessionID = run.HarnessSessionID
		if sessionID == "" {
			sessionID = cachedSession
		}
		// A restored session may come back in the agent's default mode.
		if recorded := d.s.recordedMode(entry.runID); recorded != "" {
			mode = recorded
		}
	}
	exec, err := managed.StartExecPipe(ctx, cid, runtime.ExecSpec{
		Argv: adapter, WorkingDir: d.s.cfg.WorktreeMount, CreationKey: "acp-" + rand.Text(),
		// A container started for a Standard run lacks both.
		Env: []string{coordtransport.EnhancedEnv + "=1", "NO_BROWSER=1"},
	})
	if err != nil {
		return fmt.Errorf("start %s: %w", adapter[0], err)
	}
	identity := exec.Identity()
	d.record(entry, func() { entry.agentExec = &identity })
	att := exec.Attachment()
	stderr := &tailBuffer{max: acpStderrTail}
	go func() { _, _ = io.Copy(stderr, att.Stderr()) }()

	var titleLookup time.Duration
	if profile.ACPTitleLookup {
		titleLookup = acpTitleLookupDelay
	}
	runID := entry.runID
	instructions := d.s.runInstructions(d.s.runGuide(ctx, runID, entry.missionAssigned, oneShot, true))
	prompt, systemPrompt := task, instructions
	if !profile.ACPSystemPrompt {
		prompt, systemPrompt = withInstructions(task, instructions), ""
	}
	var mcp []acp.McpServer
	if coordDir != "" {
		// Set either way: a container first launched for Standard has it.
		card := acp.EnvVariable{Name: coordtransport.RoleCardEnv}
		if systemPrompt != "" {
			card.Value = "1"
		}
		mcp = []acp.McpServer{{Stdio: &acp.McpServerStdio{
			Name: "aether", Command: coordtransport.BinaryPath, Args: []string{"mcp"}, Env: []acp.EnvVariable{card},
		}}}
	}
	sess, err := acphost.Start(ctx, att.Stdout(), att.Stdin(), acphost.Config{
		LogPath:        d.s.cfg.PTY.ItemLogPath(runID),
		Cwd:            d.s.cfg.WorktreeMount,
		MCPServers:     mcp,
		SessionID:      sessionID,
		RequireRestore: how == openSwitch || how == openContinue,
		Logger:         slog.Default().With("run", runID),
		AutoAllow:      oneShot,
		SystemPrompt:   systemPrompt,
		ClaudeCode:     profile.ACPClaudeCode,
		AgentCommand:   adapter[0],
		TitleLookup:    titleLookup,
		OnState: func(st acphost.TurnState) {
			if message, ok := acphost.UsageLimit(st.Failed); ok {
				d.s.mu.Lock()
				d.s.setUsageLimitLocked(entry, message)
				d.s.mu.Unlock()
				// The move stops this session, which waits for this
				// callback to return.
				d.s.goMove(func() { d.usageLimit(entry, oneShot, st, message) })
				return
			}
			d.turnState(entry, oneShot, st)
		},
		OnInputs: func(pending []domain.RunInputRequest) {
			d.report(runID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{{Operation: "replace", Requests: pending}}})
		},
		OnActivity: func(kind, target string) { d.activity(entry, kind, target) },
		OnTitle:    func(title string) { d.s.setRunTitle(runID, title) },
	})
	if err != nil {
		if stopErr := d.stopExec(exec); stopErr != nil {
			slog.Warn("scheduler: stop the failed ACP server", "run", runID, "error", stopErr)
		}
		return adapterError(fmt.Errorf("open the agent session: %w", err), stderr)
	}
	if id := sess.SessionID(); id != run.HarnessSessionID {
		if err := d.s.cfg.Store.SetRunAgentSession(ctx, runID, id); err != nil {
			slog.Warn("scheduler: record agent session", "run", runID, "error", err)
		}
	}
	d.record(entry, func() { entry.agentSessionID = sess.SessionID() })
	if mode != "" && sess.State().Mode != mode {
		if err := sess.SetMode(ctx, mode); err != nil {
			slog.Warn("scheduler: set the agent's mode", "run", runID, "mode", mode, "error", err)
		}
	}
	var modelErr error
	if run.Model != "" {
		if modelErr = sess.SetModel(ctx, run.Model); modelErr != nil {
			if err := sess.Notice("error", "Model not set", modelErr.Error()); err != nil {
				slog.Warn("scheduler: record the model notice", "run", runID, "error", err)
			}
		}
	}
	r := &acpRun{session: sess, exec: exec, stderr: stderr}
	// A session that started anew, here or after a failed restore, or
	// refused its prompts so far has not had them yet.
	if task == "" && systemPrompt == "" && (fresh || !acphost.Answered(sess.Log())) {
		r.instructions = instructions
	}
	if !oneShot {
		// Before the watch starts, so its first turn state is not
		// overwritten.
		d.reconnected(entry)
	}
	d.setRun(runID, r)
	go d.watch(entry, r)
	if c := d.s.coordinationSeam(); c != nil && c.enabled && !oneShot {
		c.svc.EnhancedSessionOpened(ctx, runID)
	}
	switch {
	case oneShot && marked:
		// The move a persisted usage limit owes after a restart decides
		// how the failed turn ends.
	case oneShot && modelErr != nil:
		// Unattended, the task would otherwise run on the agent's default model.
		d.endOneShot(entry, false, true, agentstatus.Report{State: agentstatus.Idle, Reason: "model not set: " + modelErr.Error()})
	case how == openContinue:
	case oneShot:
		d.startOneShot(ctx, entry, sess, fresh, prompt)
	case fresh && task != "":
		if _, err := sess.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(prompt)}, false, nil); err != nil {
			slog.Warn("scheduler: send the task to the agent", "run", runID, "error", err)
		}
	case !fresh:
		// The restore's idle report can run before setRun, when the run
		// does not look idle yet.
		d.wakeIdle(runID)
	}
	return nil
}

// reconnected clears the disconnect the run's previous session reported, so
// the run is not shown disconnected until the reopened session's first turn
// state.
func (d *acpDriver) reconnected(entry *supervised) {
	d.s.mu.Lock()
	ended := entry.agentReport.State == agentstatus.Idle && disconnectedReason(entry.agentReport.Reason)
	d.s.mu.Unlock()
	if ended {
		d.report(entry.runID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle})
	}
}

func (d *acpDriver) setRun(run domain.RunID, r *acpRun) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs[run] = r
	if r.session != nil {
		if w := d.waiters[run]; w != nil {
			close(w.ch)
			delete(d.waiters, run)
		}
	}
}

func (d *acpDriver) fail(entry *supervised, err error) {
	slog.Warn("scheduler: enhanced session failed", "run", entry.runID, "error", err)
	d.setRun(entry.runID, &acpRun{err: err})
	d.notice(entry.runID, "error", "Enhanced session failed", err.Error())
	idle := agentstatus.Report{State: agentstatus.Idle, Reason: acpFailedReason + err.Error()}
	if d.oneShot(entry) {
		d.endOneShot(entry, false, false, idle)
		return
	}
	go d.report(entry.runID, idle)
}

func (d *acpDriver) oneShot(entry *supervised) bool {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	return entry.launchMode == domain.LaunchHeadless
}

func (d *acpDriver) switchNotice(run domain.RunID, severity, title, description string) {
	defer d.lockOp(run)()
	d.notice(run, severity, title, description)
}

// notice appends to the item log of a run with no live session. The caller
// holds the run's op lock, so no session opens the log meanwhile.
func (d *acpDriver) notice(run domain.RunID, severity, title, description string) {
	log, err := acphost.OpenLog(d.s.cfg.PTY.ItemLogPath(run))
	if err != nil {
		slog.Warn("scheduler: open item log", "run", run, "error", err)
		return
	}
	if err := log.Append(&acphost.Item{Kind: acphost.KindNotice, Notice: &acphost.Notice{Severity: severity, Title: title, Description: description}}); err != nil {
		slog.Warn("scheduler: record notice", "run", run, "error", err)
	}
	if err := log.Close(); err != nil {
		slog.Warn("scheduler: close item log", "run", run, "error", err)
	}
}

func (d *acpDriver) watch(entry *supervised, r *acpRun) {
	<-r.session.Done()
	d.mu.Lock()
	stopping := r.stopping
	if d.runs[entry.runID] == r {
		r.session = nil
	}
	d.mu.Unlock()
	if stopping {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace+10*time.Second)
	defer cancel()
	status, err := r.exec.Wait(ctx)
	cause := fmt.Errorf("the agent's ACP server exited with code %d", status.Code)
	if err != nil {
		cause = fmt.Errorf("the agent's ACP server closed its output: %w", err)
		_, _ = r.exec.Stop(ctx, acpStopGrace)
	}
	_ = r.exec.Detach()
	cause = adapterError(cause, r.stderr)
	defer d.lockOp(entry.runID)()
	d.mu.Lock()
	current := d.runs[entry.runID] == r && !r.stopping && !r.ended
	if current {
		r.err = cause
	}
	d.mu.Unlock()
	if !current {
		return
	}
	d.notice(entry.runID, "error", "Enhanced session ended", cause.Error())
	idle := agentstatus.Report{State: agentstatus.Idle, Reason: acpEndedReason + cause.Error()}
	if d.oneShot(entry) {
		d.endOneShot(entry, false, false, idle)
		return
	}
	d.report(entry.runID, idle)
}

func adapterError(err error, stderr *tailBuffer) error {
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		return fmt.Errorf("%w; stderr: %s", err, tail)
	}
	return err
}

func (d *acpDriver) stopAdapter(ctx context.Context, run domain.RunID) error {
	defer d.lockOp(run)()
	return d.stopAdapterLocked(ctx, run)
}

// stopAdapterLocked keeps the adapter's exec identity in the sidecar when the
// adapter may still be running, so a later open stops it.
func (d *acpDriver) stopAdapterLocked(ctx context.Context, run domain.RunID) error {
	d.mu.Lock()
	r := d.runs[run]
	delete(d.runs, run)
	if r != nil {
		r.stopping = true
	}
	d.mu.Unlock()
	if r == nil || r.exec == nil {
		return nil
	}
	if r.session != nil {
		_ = r.session.Close()
		select {
		case <-r.session.Done():
		case <-time.After(acpStopGrace):
		case <-ctx.Done():
		}
	}
	if err := d.stopExec(r.exec); err != nil {
		return err
	}
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if entry := d.s.runs[run]; entry != nil && entry.agentExec != nil && entry.agentExec.ExecID == r.exec.Identity().ExecID {
		entry.agentExec = nil
		if err := d.s.writeSidecar(entry.sidecar()); err != nil {
			slog.Warn("scheduler: persist stopped ACP server", "run", run, "error", err)
		}
	}
	return nil
}

// shutdown closes every session without stopping its adapter or reporting
// its end, as the end of the server process would; the next server replaces
// the adapters and reports the restored sessions' state. It waits for each
// session's callbacks, so a dropped queued prompt is recorded before the
// store closes.
func (d *acpDriver) shutdown() {
	d.mu.Lock()
	d.closed = true
	var sessions []*acphost.Session
	for _, r := range d.runs {
		r.stopping = true
		if r.session != nil {
			_ = r.session.Close()
			sessions = append(sessions, r.session)
		}
	}
	d.mu.Unlock()
	deadline := time.After(acpStopGrace)
	for _, session := range sessions {
		select {
		case <-session.Done():
		case <-deadline:
			return
		}
	}
}

func (d *acpDriver) stopExec(exec runtime.ManagedExec) error {
	ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace+10*time.Second)
	defer cancel()
	defer func() { _ = exec.Detach() }()
	_, err := exec.Stop(ctx, acpStopGrace)
	if err == nil {
		return nil
	}
	if state, statusErr := exec.Status(ctx); statusErr == nil && !state.Running {
		return nil
	}
	return fmt.Errorf("stop the agent's ACP server (exec %s): %w", exec.Identity().ExecID, err)
}

// stopStaleExec stops an adapter a previous server process started: Docker
// cannot reattach its stdio, so it can only be replaced.
func (d *acpDriver) stopStaleExec(ctx context.Context, entry *supervised) {
	d.s.mu.Lock()
	stale := entry.agentExec
	d.s.mu.Unlock()
	if stale == nil {
		return
	}
	if managed, ok := d.s.cfg.Runtime.(runtime.ManagedExecRuntime); ok {
		if exec, err := managed.RecoverExec(ctx, *stale); err == nil {
			if stopErr := d.stopExec(exec); stopErr != nil {
				slog.Warn("scheduler: stop the earlier ACP server", "run", entry.runID, "error", stopErr)
			}
		} else {
			slog.Info("scheduler: earlier ACP server is gone", "run", entry.runID, "error", err)
		}
	}
	d.record(entry, func() { entry.agentExec = nil })
}

func (d *acpDriver) record(entry *supervised, change func()) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	change()
	if d.s.runs[entry.runID] != entry {
		return
	}
	if err := d.s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist enhanced session", "run", entry.runID, "error", err)
	}
}

// report hands the session's state to ReportAgentState. A launch marks its
// run running only after the session opened, so a report that arrives first
// waits for that.
func (d *acpDriver) report(run domain.RunID, report agentstatus.Report) {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return
	}
	deadline := time.Now().Add(acpReportWait)
	for {
		err := d.s.ReportAgentState(context.Background(), run, report)
		if err == nil {
			return
		}
		d.s.mu.Lock()
		entry := d.s.runs[run]
		provisioning := entry != nil && entry.status == domain.RunProvisioning
		d.s.mu.Unlock()
		if !provisioning || time.Now().After(deadline) {
			slog.Debug("scheduler: enhanced session report not applied", "run", run, "error", err)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (d *acpDriver) activity(entry *supervised, kind, target string) {
	d.s.publish(context.Background(), events.Event{
		WorkspaceID: entry.workspaceID,
		RunID:       entry.runID,
		Payload: events.AgentEventPayload{
			Kind: events.AgentToolCall, Tool: kind, Verb: acphost.ToolVerb(kind), Detail: truncateRunes(target, 200),
		},
	})
}

func (d *acpDriver) resumeAfterPause(ctx context.Context, entry *supervised) {
	if d.session(entry.runID) == nil {
		d.connect(ctx, entry, openRestore)
		return
	}
	d.wakeIdle(entry.runID)
}

func (d *acpDriver) wakeIdle(run domain.RunID) {
	if c := d.s.coordinationSeam(); c != nil && c.enabled {
		c.svc.WakeIdle(run)
	}
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
