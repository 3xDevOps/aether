package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// maxRetries is how many consecutive failed downloads a pending update
// gets before it is recorded failed.
const maxRetries = 3

// Tick is the idle poll: the scheduler calls it once per poll interval. A
// pending update applies the first time the server is idle, and applying
// it does not return - the process re-executes on the new binary.
//
// The pending row is read first, so a deployment with no update waiting
// never pays for the run-table scan behind Config.Busy.
func (s *Service) Tick(ctx context.Context) {
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	if err != nil {
		slog.Warn("serverupdate: read pending update", "error", err)
		return
	}
	if state.Pending == nil {
		return
	}
	// A pending update can only be scheduled by a capable server, but the
	// row outlives the process: an install moved to unprivileged mode
	// between the schedule and the restart leaves one that can never
	// apply. Retire it with the reason rather than refusing it quietly
	// every poll interval for the rest of time.
	if reason := s.incapableReason(); reason != "" {
		s.retire(ctx, *state.Pending, reason)
		return
	}
	if busy := s.busyNow(ctx); !busy.Idle() {
		return
	}
	restart, err := s.apply(ctx, *state.Pending, false, true)
	if err != nil {
		if !errors.Is(err, ErrBusy) && !errors.Is(err, ErrHeld) {
			slog.Error("serverupdate: scheduled update failed", "version", state.Pending.Version, "error", err)
		}
		return
	}
	restart()
}

// retire records a pending update that can never apply and clears it, so
// an admin reads the reason on server.update_status instead of watching a
// pending update that never lands.
func (s *Service) retire(ctx context.Context, pending store.PendingServerUpdate, reason string) {
	detail := "this server can no longer update itself: " + reason
	slog.Error("serverupdate: retiring a pending update", "version", pending.Version, "reason", reason)
	s.record(ctx, pending.Version, store.ServerUpdateFailed, detail)
	s.publish(ctx, pending.RequestedBy, events.ServerUpdateFailed, pending.Version, detail)
}

// busyNow asks the run engine what it is doing. A deployment wired without
// one - anything embedding the server for a test - has nothing to wait for.
func (s *Service) busyNow(ctx context.Context) domain.ServerBusy {
	if s.cfg.Busy == nil {
		return domain.ServerBusy{}
	}
	return s.cfg.Busy(ctx)
}

// apply checks, downloads, and swaps the release, and returns the restart,
// which re-executes the server and never returns on success. The update is
// pending until the swap is recorded, so a process that dies mid-download
// retries it at the next idle moment; a scheduled update that cannot
// reach the release is retried up to maxRetries polls. Any other failure leaves the
// running binaries untouched, clears the pending row, and is reported as
// `last` on server.update_status.
func (s *Service) apply(ctx context.Context, pending store.PendingServerUpdate, allowDowngrade, scheduled bool) (func(), error) {
	// The invariant every caller depends on, checked where it is used
	// rather than at each call site: nothing downloads a release or
	// replaces a binary on a server that cannot restart onto it.
	if reason := s.incapableReason(); reason != "" {
		return nil, fmt.Errorf("%w: %s", ErrIncapable, reason)
	}
	if s.cfg.Hold != nil {
		if reason := s.cfg.Hold(); reason != "" {
			return nil, fmt.Errorf("%w: %s", ErrHeld, reason)
		}
	}
	if !s.claim(pending.Version) {
		return nil, ErrBusy
	}
	if !scheduled {
		if err := s.cfg.Store.SetPendingServerUpdate(ctx, &pending); err != nil {
			s.release()
			return nil, err
		}
	}
	s.publish(ctx, pending.RequestedBy, events.ServerUpdatePreflight, pending.Version, "")
	m, err := s.preflight(ctx, pending.Version, allowDowngrade)
	var rec *swapRecord
	if err == nil {
		s.setPhase(protocol.ServerUpdatePhaseApplying)
		s.publish(ctx, pending.RequestedBy, events.ServerUpdateApplying, pending.Version, "")
		rec, err = s.swap(ctx, pending, m)
	}
	if err != nil {
		var r retryable
		if scheduled && errors.As(err, &r) && s.retry() {
			slog.Warn("serverupdate: release unreachable; retrying at the next idle poll", "version", pending.Version, "error", err)
			s.release()
			return nil, err
		}
		return nil, s.fail(ctx, pending, err)
	}
	slog.Info("serverupdate: binaries replaced", "version", pending.Version, "paths", rec.Binaries)
	if err := s.cfg.Store.SetPendingServerUpdate(ctx, nil); err != nil {
		slog.Warn("serverupdate: clear the applied pending update", "error", err)
	}
	// The restart runs after the RPC response has been written, by which
	// point the request context may be on its way out; the restarting
	// phase still has to reach the feed, so it is detached from it.
	restartCtx := context.WithoutCancel(ctx)
	return func() { s.restart(restartCtx, pending, rec) }, nil
}

// retryable marks a failure reaching the release - its manifest or a
// binary - which a scheduled update retries.
type retryable struct{ error }

func (r retryable) Unwrap() error { return r.error }

// retry counts one more failed attempt at the pending update and reports
// whether it gets another.
func (s *Service) retry() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries++
	return s.retries < maxRetries
}

// swap snapshots the database when the release migrates it, keeps each
// binary as <path>.prev, installs the release, and writes the swap record
// the next start reads. A failure restores whatever it replaced.
func (s *Service) swap(ctx context.Context, pending store.PendingServerUpdate, m selfupdate.Manifest) (*swapRecord, error) {
	rec := &swapRecord{
		Phase:       recordVerifying,
		FromVersion: version.Version,
		ToVersion:   pending.Version,
		RequestedBy: string(pending.RequestedBy),
		Binaries:    []string{s.self},
		At:          s.cfg.Now().UTC(),
	}
	if sibling := filepath.Join(filepath.Dir(s.self), "aether"); fileExists(sibling) {
		rec.Binaries = append(rec.Binaries, sibling)
	}
	schema, err := s.cfg.Store.SchemaVersion(ctx)
	if err != nil {
		return nil, err
	}
	if s.cfg.Database != "" && m.StoreSchema > schema {
		snap := snapshotPath(s.cfg.StateDir)
		if err = os.MkdirAll(s.cfg.StateDir, 0o700); err != nil {
			return nil, err
		}
		if err = os.Remove(snap); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove the last database snapshot: %w", err)
		}
		if err = s.cfg.Store.Snapshot(ctx, snap); err != nil {
			return nil, err
		}
		rec.Database, rec.Snapshot, rec.SchemaBefore = s.cfg.Database, snap, schema
	}
	if err = keepPrevious(rec.Binaries); err != nil {
		return nil, err
	}
	replaced, err := selfupdate.UpdateBinaries(ctx,
		s.cfg.Checker.BaseURL(), pending.Version, s.self, "aether-server", "aether")
	if err != nil {
		if rerr := restorePrevious(replaced); rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		return nil, retryable{err}
	}
	if err := saveRecord(s.cfg.StateDir, rec); err != nil {
		return nil, errors.Join(err, restorePrevious(rec.Binaries))
	}
	return rec, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// fail records a failed attempt, which clears the pending row, and frees
// the applying slot.
func (s *Service) fail(ctx context.Context, pending store.PendingServerUpdate, err error) error {
	msg := err.Error()
	s.release()
	s.record(ctx, pending.Version, store.ServerUpdateFailed, msg)
	s.publish(ctx, pending.RequestedBy, events.ServerUpdateFailed, pending.Version, msg)
	return err
}

// claim takes the one applying slot, so the RPC path and the idle poll can
// never download over each other.
func (s *Service) claim(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applying {
		return false
	}
	s.applying, s.phase, s.target = true, protocol.ServerUpdatePhasePreflight, target
	return true
}

func (s *Service) setPhase(phase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = phase
}

func (s *Service) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applying, s.phase, s.target = false, "", ""
}

// restart re-executes the new binary in place. syscall.Exec keeps the PID,
// which is what the shipped `Restart=on-failure` unit needs - a clean exit
// would not be restarted - and is also what makes a foreground
// `aether-server serve` come back. Runs survive because the scheduler
// reattaches to live containers on boot; attached terminals and live syncs
// drop and reconnect. With Config.Drain the exec happens in
// cmd/aether-server once the server has closed.
func (s *Service) restart(ctx context.Context, pending store.PendingServerUpdate, rec *swapRecord) {
	s.setPhase(protocol.ServerUpdatePhaseRestarting)
	s.publish(ctx, pending.RequestedBy, events.ServerUpdateRestarting, pending.Version, "")
	argv := append([]string{s.self}, os.Args[1:]...)
	if s.cfg.Drain != nil {
		s.cfg.Drain(argv)
		return
	}
	err := s.cfg.Host.Exec(s.self, argv, os.Environ())
	if err == nil {
		// syscall.Exec never returns on success, so reaching here at all
		// means the process was not replaced, whatever the seam reported.
		err = errors.New("returned without replacing the process")
	}
	// Under systemd the unit can still be restarted onto the new binary,
	// whose own start decides whether it stays. Anywhere else nothing
	// will start it, so the swap is undone here.
	if s.cfg.Host.UnderSystemd() {
		slog.Error("serverupdate: re-exec failed, asking systemd to restart", "error", err)
		rerr := s.cfg.Host.Restart()
		if rerr == nil {
			return
		}
		err = fmt.Errorf("re-exec failed (%v) and systemctl restart failed: %w", err, rerr)
	} else {
		err = fmt.Errorf("re-exec %s: %w", s.self, err)
	}
	s.undo(ctx, pending, rec, err)
}

// undo restores the previous binaries after a restart that never reached
// the new one. Nothing migrated the database, so the snapshot is dropped.
func (s *Service) undo(ctx context.Context, pending store.PendingServerUpdate, rec *swapRecord, cause error) {
	detail := fmt.Sprintf("%v; restored %s", cause, rec.FromVersion)
	if err := restorePrevious(rec.Binaries); err != nil {
		detail = fmt.Sprintf("%v; restoring %s failed: %v", cause, rec.FromVersion, err)
	}
	if rec.Snapshot != "" {
		_ = os.Remove(rec.Snapshot)
		rec.Snapshot = ""
	}
	rec.Phase, rec.Error, rec.Recorded, rec.At = recordRolledBack, detail, true, s.cfg.Now().UTC()
	if err := saveRecord(s.cfg.StateDir, rec); err != nil {
		slog.Error("serverupdate: write the swap record", "error", err)
	}
	s.release()
	s.record(ctx, pending.Version, store.ServerUpdateRolledBack, detail)
	s.publish(ctx, pending.RequestedBy, events.ServerUpdateRolledBack, pending.Version, detail)
}

// record stores the attempt's outcome, which also clears the pending row.
func (s *Service) record(ctx context.Context, version, outcome, detail string) {
	s.mu.Lock()
	s.retries = 0
	s.mu.Unlock()
	if err := s.cfg.Store.SetLastServerUpdate(ctx, store.ServerUpdateAttempt{
		Version: version,
		Outcome: outcome,
		Detail:  detail,
		At:      s.cfg.Now().UTC(),
	}); err != nil {
		slog.Error("serverupdate: record update outcome", "outcome", outcome, "error", err)
	}
}

func (s *Service) publish(ctx context.Context, actor domain.MemberID, phase events.ServerUpdatePhase, target, detail string) {
	publish(ctx, s.cfg.Store, s.cfg.Bus, events.ServerUpdatePayload{
		Phase: phase, Version: target, FromVersion: version.Version, ActorID: actor, Detail: detail,
	})
}

// publish stamps one phase into every workspace's timeline. A server
// update touches every workspace, and the timeline is per-workspace, so
// the act is attributed everywhere it is felt. A server with no workspaces
// yet has nowhere to record it; server.update_status still reports the
// phase.
func publish(ctx context.Context, st store.Store, bus events.Bus, payload events.ServerUpdatePayload) {
	workspaces, err := st.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("serverupdate: list workspaces for the update feed", "error", err)
		return
	}
	for _, ws := range workspaces {
		if _, perr := bus.Publish(ctx, events.Event{
			WorkspaceID: ws.ID,
			ActorID:     payload.ActorID,
			Payload:     payload,
		}); perr != nil {
			slog.Warn("serverupdate: publish update phase", "phase", payload.Phase, "workspace", ws.ID, "error", perr)
		}
	}
}
