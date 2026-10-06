package scheduler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

var ErrNotSwitchable = errors.New("scheduler: this agent cannot move a running session between Standard and Enhanced")

var (
	ErrSessionNotReported  = fmt.Errorf("%w: the agent has not reported its session yet; it does on its first turn", ErrInvalidTransition)
	ErrAdapterNotInstalled = fmt.Errorf("%w: the agent's ACP server is not installed; install it with Enhanced selected on the Agents page", ErrInvalidTransition)
)

var ErrSwitching = errors.New("scheduler: the run is switching between Standard and Enhanced")

// tuiSettle is how long a resumed terminal must keep running for a switch to
// Standard to count: a CLI that cannot resume the session exits at once.
const tuiSettle = 3 * time.Second

func modeName(mode domain.LaunchMode) string {
	if mode == domain.LaunchACP {
		return "Enhanced"
	}
	return "Standard"
}

func (s *Scheduler) Switching(run domain.RunID) domain.LaunchMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.runs[run]; entry != nil {
		return entry.switching
	}
	return ""
}

func (s *Scheduler) AgentSwitchable(ctx context.Context, member, account domain.MemberID, harnessName string) (bool, error) {
	profile, _, err := s.launchProfile(ctx, member, account, harnessName)
	if err != nil {
		return false, err
	}
	return profile.Switchable(), nil
}

// SwitchMode moves a live run's agent between its terminal (tui) and its ACP
// server (acp) inside the same container, resuming the agent's session.
// admit calls begin, which claims the run for the switch, under the caller's
// authorization. A failed switch puts the previous mode's driver back and
// returns the real error.
func (s *Scheduler) SwitchMode(ctx context.Context, run domain.RunID, actor domain.MemberID, mode domain.LaunchMode, admit func(begin func() error) error) error {
	if mode != domain.LaunchTUI && mode != domain.LaunchACP {
		return fmt.Errorf("%w: a run switches to tui or acp, not %q", ErrInvalidTransition, mode)
	}
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return err
	}
	profile, argvs, err := s.launchProfile(ctx, r.MemberID, r.AccountMember(), r.Harness)
	if err != nil {
		return err
	}
	if !profile.Switchable() {
		return fmt.Errorf("%w: %s", ErrNotSwitchable, r.Harness)
	}
	if mode == domain.LaunchACP && s.cfg.Homes != nil {
		installed, lookErr := s.adapterInstalled(r.MemberID, r.AccountMember(), profile, argvs)
		if lookErr != nil {
			return fmt.Errorf("look for the agent's ACP server: %w", lookErr)
		}
		if !installed {
			return ErrAdapterNotInstalled
		}
	}
	c := s.coordinationSeam()
	if c == nil || c.svc == nil {
		return fmt.Errorf("%w: switching needs the run directory, and coordination is not configured", ErrInvalidTransition)
	}
	s.mu.Lock()
	entry := s.runs[run]
	s.mu.Unlock()
	if entry == nil {
		return fmt.Errorf("%w: the run has no live container", ErrInvalidTransition)
	}
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	var from domain.LaunchMode
	var session string
	var cid runtime.ID
	err = admit(func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if refusal := s.switchableLocked(entry, mode); refusal != nil {
			return refusal
		}
		session = entry.agentSessionID
		if session == "" {
			session = r.HarnessSessionID
		}
		switch {
		case session == "":
			return ErrSessionNotReported
		case !domain.ValidAgentSessionID(session):
			return fmt.Errorf("%w: the agent's session id %q cannot be resumed", ErrInvalidTransition, session)
		}
		from, cid = entry.launchMode, entry.containerID
		entry.switching = mode
		return nil
	})
	if err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	s.publishMode(ctx, entry, actor, events.RunModePayload{Mode: mode, Previous: from, Switching: mode, Reason: "Switching to " + modeName(mode) + "…"})
	sw := modeSwitch{s: s, c: c, entry: entry, run: r, profile: profile, cid: cid, session: session}
	if mode == domain.LaunchTUI {
		err = sw.toStandard(ctx)
	} else {
		err = sw.toEnhanced(ctx)
	}
	s.mu.Lock()
	entry.switching = ""
	now := entry.launchMode
	s.mu.Unlock()
	if now == domain.LaunchACP {
		s.acp.wakeIdle(run)
	}
	done := events.RunModePayload{Mode: now, Previous: from}
	if err != nil {
		err = fmt.Errorf("switch to %s: %w", modeName(mode), err)
		done.Reason = err.Error()
	}
	s.publishMode(ctx, entry, actor, done)
	return err
}

func (s *Scheduler) switchableLocked(entry *supervised, mode domain.LaunchMode) error {
	switch {
	case s.runs[entry.runID] != entry || entry.containerID == "" || entry.retained || entry.destroyPending ||
		entry.finalizing || entry.exitObserved || entry.killRequested:
		return fmt.Errorf("%w: the run's container is not live", ErrInvalidTransition)
	case entry.status != domain.RunRunning && entry.status != domain.RunNeedsAttention:
		return fmt.Errorf("%w: the run is %s", ErrInvalidTransition, entry.status)
	case entry.paused:
		return fmt.Errorf("%w: the run is paused; resume it first", ErrInvalidTransition)
	case !entry.launchMode.Interactive():
		return fmt.Errorf("%w: a background run cannot switch modes", ErrInvalidTransition)
	case entry.launchMode == mode:
		return fmt.Errorf("%w: the run is already %s", ErrInvalidTransition, modeName(mode))
	case entry.coordDir == "":
		return fmt.Errorf("%w: the run has no run directory for its supervisor to read", ErrInvalidTransition)
	}
	return nil
}

func (s *Scheduler) publishMode(ctx context.Context, entry *supervised, actor domain.MemberID, payload events.RunModePayload) {
	s.publish(ctx, events.Event{WorkspaceID: entry.workspaceID, RunID: entry.runID, ActorID: actor, Payload: payload})
}

// The caller of a modeSwitch method holds the run's lifecycleMu.
type modeSwitch struct {
	s       *Scheduler
	c       *coordination
	entry   *supervised
	run     *domain.Run
	profile harness.Profile
	cid     runtime.ID
	session string
}

func (m modeSwitch) toStandard(ctx context.Context) error {
	nonce, reporter, err := m.writeTerminal()
	if err != nil {
		return err
	}
	if err := m.s.acp.stopAdapter(ctx, m.entry.runID); err != nil {
		return errors.Join(err, m.reopenEnhanced(ctx))
	}
	if err := m.recordIntent(&switchIntent{Mode: domain.LaunchTUI, Nonce: nonce, Reporter: reporter}); err != nil {
		return errors.Join(err, m.reopenEnhanced(ctx))
	}
	if err := m.s.swapChild(ctx, m.cid, nonce, tuiSettle); err != nil {
		return errors.Join(err, m.restoreEnhanced(ctx))
	}
	m.s.acp.switchNotice(m.entry.runID, "info", "Switched to Standard",
		"The conversation continues in the agent's terminal and is not recorded here until the run switches back to Enhanced.")
	if err := m.s.commitMode(ctx, m.entry, domain.LaunchTUI, reporter); err != nil {
		return err
	}
	if err := m.s.ReportAgentState(ctx, m.entry.runID, agentstatus.Report{
		State: agentstatus.Idle, Reason: agentstatus.ReasonIdle,
		InputUpdates: []domain.RunInputUpdate{{Operation: "replace", Requests: []domain.RunInputRequest{}}},
	}); err != nil {
		slog.Warn("scheduler: report the switched run idle", "run", m.entry.runID, "error", err)
	}
	return nil
}

func (m modeSwitch) toEnhanced(ctx context.Context) error {
	nonce, err := m.writeShell()
	if err != nil {
		return err
	}
	if err := m.recordIntent(&switchIntent{Mode: domain.LaunchACP, Nonce: nonce, Reporter: harness.ReporterFull}); err != nil {
		return err
	}
	if err := m.s.swapChild(ctx, m.cid, nonce, 0); err != nil {
		return errors.Join(fmt.Errorf("stop the agent's terminal: %w", err), m.restoreStandard(ctx))
	}
	m.s.acp.switchNotice(m.entry.runID, "info", "Switched from Standard",
		"Turns the agent took in its terminal are not shown here.")
	if err := m.s.acp.openForSwitch(ctx, m.entry); err != nil {
		m.s.acp.switchNotice(m.entry.runID, "error", "Switch to Enhanced failed", err.Error())
		return errors.Join(err, m.restoreStandard(ctx))
	}
	return m.s.commitMode(ctx, m.entry, domain.LaunchACP, harness.ReporterFull)
}

// restoreEnhanced and restoreStandard keep the switch's intent until the
// swap back is confirmed: a swap that failed may still have happened.
func (m modeSwitch) restoreEnhanced(ctx context.Context) error {
	nonce, err := m.writeShell()
	if err == nil {
		err = m.s.swapChild(ctx, m.cid, nonce, 0)
	}
	if err != nil {
		err = fmt.Errorf("start the login shell: %w", err)
	} else {
		err = m.recordIntent(nil)
	}
	return errors.Join(err, m.reopenEnhanced(ctx))
}

// reopenEnhanced restores the session the adapter had. A restore that fails
// leaves the Enhanced session failed, as an adapter crash would.
func (m modeSwitch) reopenEnhanced(ctx context.Context) error {
	m.s.acp.connect(ctx, m.entry, openSwitch)
	if _, err := m.s.acp.live(m.entry.runID); err != nil {
		return fmt.Errorf("restore the enhanced session: %w", err)
	}
	return nil
}

func (m modeSwitch) restoreStandard(ctx context.Context) error {
	nonce, _, err := m.writeTerminal()
	if err == nil {
		err = m.s.swapChild(ctx, m.cid, nonce, tuiSettle)
	}
	if err != nil {
		return fmt.Errorf("restore the agent's terminal: %w", err)
	}
	return m.recordIntent(nil)
}

// recordIntent persists the child swap about to happen, so a server that
// stops before commitMode can tell which mode the container ended up in.
func (m modeSwitch) recordIntent(intent *switchIntent) error {
	m.s.mu.Lock()
	defer m.s.mu.Unlock()
	previous := m.entry.switchIntent
	m.entry.switchIntent = intent
	if err := m.s.writeSidecar(m.entry.sidecar()); err != nil {
		m.entry.switchIntent = previous
		return fmt.Errorf("persist the switch: %w", err)
	}
	return nil
}

// writeTerminal returns the next-command file's nonce and the reporter the
// command gives the run.
func (m modeSwitch) writeTerminal() (string, harness.Reporter, error) {
	argv := m.profile.ResumeCommand(m.session)
	var native harness.NativeLaunch
	if m.c.enabled && m.run.Task != "" {
		var err error
		if native, err = m.profile.PrepareNativeLaunch(coordtransport.MountDir, argv, nil); err != nil {
			return "", 0, fmt.Errorf("prepare native coordination: %w", err)
		}
	}
	tui := *m.run
	tui.Mode = domain.LaunchTUI
	launch, err := newCoordinationLaunch(m.c.enabled, &tui, m.profile, native)
	if err != nil {
		return "", 0, err
	}
	env := make(map[string]string)
	maps.Copy(env, launch.env)
	maps.Copy(env, native.Env)
	nonce := rand.Text()
	files := maps.Clone(launch.files)
	files[coordtransport.NextCommandName] = nextCommand(nonce, native.Command(append(argv, launch.args...)), env)
	if err := m.c.svc.WriteFiles(m.entry.runID, files); err != nil {
		return "", 0, fmt.Errorf("write the agent's command: %w", err)
	}
	return nonce, launch.reporter, nil
}

func (m modeSwitch) writeShell() (string, error) {
	nonce := rand.Text()
	if err := m.c.svc.WriteFiles(m.entry.runID, map[string][]byte{coordtransport.NextCommandName: nextCommand(nonce, nil, nil)}); err != nil {
		return "", fmt.Errorf("write the shell command: %w", err)
	}
	return nonce, nil
}

// commitMode records the mode the container now runs. The intent stays until
// the run row holds the mode, so a restart retries a row that failed to save.
func (s *Scheduler) commitMode(ctx context.Context, entry *supervised, mode domain.LaunchMode, reporter harness.Reporter) error {
	acp := mode == domain.LaunchACP
	err := s.cfg.Store.SetRunMode(ctx, entry.runID, mode, acp)
	if err != nil {
		err = fmt.Errorf("record the run's mode: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.launchMode, entry.acp, entry.reporter = mode, acp, reporter
	if err == nil {
		entry.switchIntent = nil
	}
	if werr := s.writeSidecar(entry.sidecar()); werr != nil {
		err = errors.Join(err, fmt.Errorf("persist the run's mode: %w", werr))
	}
	return err
}

// settleSwitch finishes a mode switch the previous server process left
// between its child swap and commitMode: the container runs the target mode
// when the supervisor's state file names the swap's nonce. The caller holds
// the run's lifecycleMu.
func (s *Scheduler) settleSwitch(ctx context.Context, entry *supervised) {
	s.mu.Lock()
	intent, cid := entry.switchIntent, entry.containerID
	s.mu.Unlock()
	if intent == nil {
		return
	}
	_, stdout, _, err := s.cfg.Runtime.Exec(ctx, cid, []string{"/bin/sh", "-c", `cat "$1" 2>/dev/null || :`, "aether-switch", supervisorStateFile}, "")
	if err != nil {
		slog.Warn("scheduler: read the run supervisor's state", "run", entry.runID, "error", err)
		return
	}
	if nonce, _, _ := strings.Cut(stdout, " "); nonce == intent.Nonce {
		if err := s.commitMode(ctx, entry, intent.Mode, intent.Reporter); err != nil {
			slog.Warn("scheduler: finish an interrupted mode switch", "run", entry.runID, "error", err)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.switchIntent = nil
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: clear an interrupted mode switch", "run", entry.runID, "error", err)
	}
}

// switchIntent is a mode switch whose child swap may have happened.
type switchIntent struct {
	Mode     domain.LaunchMode `json:"mode"`
	Nonce    string            `json:"nonce"`
	Reporter harness.Reporter  `json:"reporter,omitempty"`
}

// nextCommand is the supervisor's next-command file: "# <nonce>", then a
// script that runs argv with env, or nothing for a login shell.
func nextCommand(nonce string, argv []string, env map[string]string) []byte {
	var b strings.Builder
	b.WriteString("# " + nonce + "\n")
	if len(argv) == 0 {
		return []byte(b.String())
	}
	b.WriteString("unset " + coordtransport.EnhancedEnv + "\n")
	for _, key := range slices.Sorted(maps.Keys(env)) {
		b.WriteString("export " + key + "=" + shellquote.QuoteAlways(env[key]) + "\n")
	}
	b.WriteString("exec")
	for _, arg := range argv {
		b.WriteString(" " + shellquote.QuoteAlways(arg))
	}
	b.WriteString("\n")
	return []byte(b.String())
}
