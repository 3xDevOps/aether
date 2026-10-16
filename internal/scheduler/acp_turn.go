package scheduler

import (
	"context"
	"log/slog"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/agentstatus"
)

// The dashboard matches these reason prefixes (web/src/lib/needs-you.ts).
const (
	acpFailedReason     = "enhanced session failed: "
	acpEndedReason      = "enhanced session ended: "
	acpTurnFailedReason = "enhanced turn failed: "
)

// Idle reasons of an interactive enhanced turn that did not end normally.
const (
	acpCancelledReason   = "turn cancelled"
	acpInterruptedReason = "turn interrupted"
	acpStoppedReason     = "turn ended: "
)

func backgroundWork(report agentstatus.Report) bool {
	return report.State == agentstatus.Working && strings.HasPrefix(report.Reason, agentstatus.ReasonBackground)
}

// heldStallFactor stretches the stall threshold while background tasks the
// run waits for are live: a task reports its tool uses, not the work between
// them.
const heldStallFactor = 3

// heldByTasks reports whether background tasks the run waits for are live.
func heldByTasks(report agentstatus.Report) bool {
	return backgroundWork(report) && report.Reason != agentstatus.ReasonBackground
}

func (d *acpDriver) turnState(entry *supervised, oneShot bool, st acphost.TurnState) {
	if oneShot && !st.Working {
		// An interrupted session is ended by its connection's end or
		// resumed by startOneShot.
		if st.Reason != "" && st.Reason != "interrupted" {
			d.endOneShot(entry, st.Reason == "end_turn", false, turnEndReport(st.Reason))
		}
		return
	}
	d.s.mu.Lock()
	entry.betweenTurns = st.Working && st.Reason == "background" && !st.Autonomous
	entry.heldTools = st.Working && st.Reason == "background" && st.Tasks > 0 && st.Agents == 0
	d.s.mu.Unlock()
	report := agentstatus.Report{State: agentstatus.Working}
	switch {
	case st.Working && st.Reason == "background":
		report.Reason = agentstatus.BackgroundReason(st.Tasks)
	case st.Working:
	case st.Failed != nil:
		// Not a turn end: a waiting outcome report must not finish the run
		// on a turn that failed.
		report = agentstatus.Report{State: agentstatus.Idle, Reason: acpTurnFailedReason + st.Failed.Error()}
	default:
		report = agentstatus.Report{State: agentstatus.Idle, Reason: acpIdleReason(st.Reason)}
	}
	d.report(entry.runID, report)
	// Between turns a mail prompt can start one; PromptIdle refuses while a
	// follow-up is owed, and the follow-up's end wakes again.
	if !st.Working || (st.Reason == "background" && !st.Autonomous) {
		d.wakeIdle(entry.runID)
	}
}

// usageLimit handles a turn that failed on the account's usage limit: the
// run continues on another account, or stops with the reason why not.
func (d *acpDriver) usageLimit(entry *supervised, oneShot bool, st acphost.TurnState, message string) {
	outcome, stopped := d.s.continueLiveRun(entry, false, message)
	switch outcome {
	case usageContinued, usageShutdown, usageSkipped:
		return
	}
	d.s.clearUsageLimit(entry)
	switch outcome {
	case usageRefused:
		d.turnState(entry, oneShot, st)
	case usageNoFallback:
		report := agentstatus.Report{State: agentstatus.Idle, Reason: stopped}
		if !oneShot {
			d.report(entry.runID, report)
			return
		}
		d.s.mu.Lock()
		entry.failReason = stopped
		d.s.mu.Unlock()
		d.endOneShot(entry, false, false, report)
	}
}

// startOneShot sends prompt, the task with the run instructions it needs.
func (d *acpDriver) startOneShot(ctx context.Context, entry *supervised, sess *acphost.Session, fresh bool, prompt string) {
	if !fresh {
		last := acphost.LastItem(sess.Log(), acphost.KindTurnEnd)
		reset := acphost.LastItem(sess.Log(), acphost.KindReset).Seq > last.Seq
		switch {
		case acphost.Interrupted(sess.Log()):
			prompt = oneShotResume
		case last.StopReason == "", last.StopReason == "interrupted" && reset:
		default:
			d.endOneShot(entry, last.StopReason == "end_turn", false, turnEndReport(last.StopReason))
			return
		}
	}
	d.mu.Lock()
	r := d.runs[entry.runID]
	if r != nil && prompt != oneShotResume {
		r.untried = true
	}
	d.mu.Unlock()
	// The session reports acceptance before the turn's end.
	accepted := func(_ bool, err error) {
		if err == nil && r != nil {
			d.mu.Lock()
			r.untried = false
			d.mu.Unlock()
		}
	}
	if _, err := sess.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(prompt)}, false, accepted); err != nil && !usageLimited(err) {
		d.endOneShot(entry, false, false, agentstatus.Report{State: agentstatus.Idle, Reason: "send the task to the agent: " + err.Error()})
	}
}

// acpIdleReason is an interactive run's reason for a turn that ended with
// stop; a restored session with no interrupted turn reports none.
func acpIdleReason(stop string) string {
	switch stop {
	case "", "end_turn":
		return agentstatus.ReasonIdle
	case "cancelled":
		return acpCancelledReason
	case "interrupted":
		return acpInterruptedReason
	}
	return acpStoppedReason + stop
}

func turnEndReport(stop string) agentstatus.Report {
	if stop == "end_turn" {
		return agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}
	}
	return agentstatus.Report{State: agentstatus.Idle, Reason: "the agent's turn ended: " + stop}
}

// Only the first call for a session ends it, and its session takes no input
// from then on. A swarm worker's run is left to CompleteMission unless
// untried: its agent never got the task, so there is nothing to judge.
func (d *acpDriver) endOneShot(entry *supervised, ok, untried bool, idle agentstatus.Report) {
	d.mu.Lock()
	r := d.runs[entry.runID]
	skip := d.closed || r == nil || r.stopping || r.ended
	live := r != nil && r.session != nil
	if !skip {
		r.ended = true
		untried = untried || (!ok && r.untried)
		r.untried = false
	}
	d.mu.Unlock()
	if skip {
		return
	}
	go func() {
		unlock := d.lockOp(entry.runID)
		d.mu.Lock()
		current := d.runs[entry.runID] == r
		d.mu.Unlock()
		if current && live {
			if err := d.stopAdapterLocked(context.Background(), entry.runID); err != nil {
				slog.Warn("scheduler: stop the background run's ACP server", "run", entry.runID, "error", err)
			}
		}
		unlock()
		d.s.mu.Lock()
		assigned, cid := entry.missionAssigned && !untried, entry.containerID
		if !ok && !assigned {
			entry.exitDetail = idle.Reason
		}
		d.s.mu.Unlock()
		if assigned {
			d.report(entry.runID, idle)
			return
		}
		signal := "USR2"
		if ok {
			signal = "USR1"
		}
		ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace+10*time.Second)
		defer cancel()
		if code, _, stderr, err := d.s.cfg.Runtime.Exec(ctx, cid, []string{"/bin/sh", "-c", "kill -" + signal + " 1"}, ""); err != nil || code != 0 {
			slog.Warn("scheduler: end the background run's container", "run", entry.runID, "code", code, "stderr", stderr, "error", err)
		}
	}()
}
