package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acpkeeper"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
)

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

// reopenLost restores the session the agent lost from the agent's own
// history, or stops it when the agent lost it again soon after, unless the
// run stopped or reopened its session meanwhile.
func (d *acpDriver) reopenLost(entry *supervised, lost *acpRun, cause error) {
	defer d.lockOp(entry.runID)()
	d.mu.Lock()
	sess := lost.session
	current := d.runs[entry.runID] == lost && !lost.stopping && sess != nil
	d.mu.Unlock()
	if !current {
		return
	}
	d.s.mu.Lock()
	again := time.Since(entry.sessionLostAt) < acpLostAgain
	entry.sessionLostAt = time.Now()
	d.s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), acpConnectTimeout)
	defer cancel()
	if again {
		if err := d.stopAdapterLocked(ctx, entry.runID); err != nil {
			slog.Warn("scheduler: stop the ACP server that lost its session", "run", entry.runID, "error", err)
		}
		d.fail(entry, fmt.Errorf("the agent lost its session again within %d minutes: %w", int(acpLostAgain.Minutes()), cause))
		return
	}
	if err := sess.Notice("warning", "Agent session lost", "The agent no longer has this session. Aether is restarting the agent and restoring the session from its history; send again any message that failed."); err != nil {
		slog.Warn("scheduler: record the lost session notice", "run", entry.runID, "error", err)
	}
	// Its keeper's agent lost the session: open must replace it even when
	// stopping it fails, never reattach.
	d.record(entry, func() { entry.agentKeeper = nil })
	if err := d.open(ctx, entry, openRestore); err != nil {
		d.fail(entry, err)
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
	if r.keeper != nil && errors.Is(r.keeper.Err(), acpkeeper.ErrPreempted) {
		r.release()
		d.takenOver(entry, r)
		return
	}
	var cause error
	switch exit := r.exit(); {
	case exit != nil:
		cause = fmt.Errorf("the agent's ACP server exited with code %d", exit.Code)
		_, _ = r.stderr.Write([]byte(exit.Stderr))
		_, _ = r.exec.Wait(ctx)
	case r.keeper != nil:
		cause = fmt.Errorf("the agent's ACP server closed its output: %w", errors.Join(r.keeper.Err(), errors.New("the keeper did not report the agent's exit")))
		_, _ = r.exec.Stop(ctx, acpStopGrace)
	default:
		status, err := r.exec.Wait(ctx)
		cause = fmt.Errorf("the agent's ACP server exited with code %d", status.Code)
		if err != nil {
			cause = fmt.Errorf("the agent's ACP server closed its output: %w", err)
			_, _ = r.exec.Stop(ctx, acpStopGrace)
		}
	}
	r.release()
	_ = r.exec.Detach()
	d.forget(entry, r.exec)
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
	d.s.mu.Lock()
	stopped := entry.stopNoticeLocked()
	d.s.mu.Unlock()
	idle := agentstatus.Report{State: agentstatus.Idle, Reason: acpEndedReason + cause.Error()}
	if stopped != "" {
		d.notice(entry.runID, "info", "Enhanced session stopped", stopped)
		idle.Reason = acpEndedReason + stopped
	} else {
		d.notice(entry.runID, "error", "Enhanced session ended", cause.Error())
	}
	if d.oneShot(entry) {
		d.endOneShot(entry, false, idle)
		return
	}
	d.report(entry.runID, idle)
}

// takenOver restarts the agent of a run whose keeper another attacher took
// over. This server process never preempts its own attachment, and only one
// server process may run on its state, so the other attacher is not trusted
// with the session.
func (d *acpDriver) takenOver(entry *supervised, lost *acpRun) {
	defer d.lockOp(entry.runID)()
	d.mu.Lock()
	current := d.runs[entry.runID] == lost && !lost.stopping && !lost.ended
	d.mu.Unlock()
	if !current {
		return
	}
	slog.Warn("scheduler: another attacher took over the agent's keeper", "run", entry.runID)
	d.notice(entry.runID, "error", "Agent connection taken over", "Another process attached to the agent's keeper, so Aether restarted the agent and restored its session.")
	// open stops the keeper the run still records as its exec.
	d.record(entry, func() { entry.agentKeeper = nil })
	ctx, cancel := context.WithTimeout(context.Background(), acpConnectTimeout)
	defer cancel()
	if err := d.open(ctx, entry, openRestore); err != nil {
		d.fail(entry, err)
	}
}

func adapterError(err error, stderr *tailBuffer) error {
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		return fmt.Errorf("%w; stderr: %s", err, tail)
	}
	return err
}

// stopAdapter also stops a keeper the run records but no live session
// holds, as after an open that failed before it reattached.
func (d *acpDriver) stopAdapter(ctx context.Context, run domain.RunID) error {
	defer d.lockOp(run)()
	err := d.stopAdapterLocked(ctx, run)
	d.s.mu.Lock()
	entry := d.s.runs[run]
	d.s.mu.Unlock()
	if err == nil && entry != nil {
		d.stopStaleExec(ctx, entry)
	}
	return err
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
		// Closing writes to the agent's stdin, which an agent that stopped
		// reading never takes; stopping the exec ends that write.
		go func() { _ = r.session.Close() }()
		select {
		case <-r.session.Done():
		case <-time.After(acpStopGrace):
		case <-ctx.Done():
		}
	}
	err := d.stopExec(r.exec)
	r.release()
	if err != nil {
		return err
	}
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if entry := d.s.runs[run]; entry != nil && entry.agentExec != nil && entry.agentExec.ExecID == r.exec.Identity().ExecID {
		entry.agentExec, entry.agentKeeper = nil, nil
		if err := d.s.writeSidecar(entry.sidecar()); err != nil {
			slog.Warn("scheduler: persist stopped ACP server", "run", run, "error", err)
		}
	}
	return nil
}

// shutdown detaches every session from its keeper without recording or
// reporting its end: the adapter, its turn and its requests run on, and the
// next server process reattaches and adopts them.
func (d *acpDriver) shutdown() {
	d.mu.Lock()
	d.closed = true
	type detaching struct {
		r    *acpRun
		sess *acphost.Session
	}
	var all []detaching
	for _, r := range d.runs {
		r.stopping = true
		if r.session != nil {
			all = append(all, detaching{r, r.session})
		}
	}
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace)
	defer cancel()
	var wg sync.WaitGroup
	for _, x := range all {
		wg.Go(func() {
			if x.r.keeper == nil {
				// An adapter run without a keeper ends with this process.
				_ = x.sess.Close()
			} else {
				// First, so no prompt starts and nothing records the end;
				// a cancel or answer written from here until the keeper
				// confirms still reaches the agent.
				x.sess.Detach()
				if err := x.r.keeper.Detach(ctx); err != nil {
					slog.Warn("scheduler: detach from the agent's keeper", "error", err)
				}
			}
			select {
			case <-x.sess.Done():
			case <-ctx.Done():
			}
			// Acks what the session handled before it ended.
			x.r.release()
		})
	}
	wg.Wait()
}
