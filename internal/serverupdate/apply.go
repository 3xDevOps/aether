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

// maxRetries is how many consecutive retryable failures a pending update
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
		if !errors.Is(err, ErrBusy) && !errors.Is(err, ErrHeld) && !errors.Is(err, errBecameBusy) {
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
// retries it at the next idle moment; a scheduled update whose preflight
// or download fails on the network is retried up to maxRetries polls, and
// one that finds the server busy again after the download is undone and
// waits for the next idle moment. Any other failure leaves the running
// binaries untouched, clears the pending row, and is reported as `last` on
// server.update_status.
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
	// The trial of the last update has not committed: a new swap record
	// would be overwritten by that Commit.
	last, err := loadRecord(s.cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if last != nil && last.Phase == recordVerifying {
		return nil, fmt.Errorf("%w: %s is still verifying its first start (%s); retry once it has started",
			ErrBusy, last.ToVersion, recordPath(s.cfg.StateDir))
	}
	if !s.claim(pending.Version) {
		return nil, ErrBusy
	}
	// A downgrade is applied by hand, now, so one cut short is not left
	// pending for an idle moment.
	if !scheduled && !allowDowngrade {
		if err = s.cfg.Store.SetPendingServerUpdate(ctx, &pending); err != nil {
			s.release()
			return nil, err
		}
	}
	s.publish(ctx, pending.RequestedBy, events.ServerUpdatePreflight, pending.Version, "")
	m, err := s.preflight(ctx, pending.Version, allowDowngrade)
	if err != nil {
		return nil, s.failOrRetry(ctx, pending, scheduled, err)
	}
	if reason := s.replacedOnDisk(); reason != "" {
		return nil, s.failOrRetry(ctx, pending, scheduled, fmt.Errorf("%w: %s", ErrIncapable, reason))
	}
	s.setPhase(protocol.ServerUpdatePhaseApplying)
	s.publish(ctx, pending.RequestedBy, events.ServerUpdateApplying, pending.Version, "")
	rec, err := s.swap(ctx, pending, m)
	if err != nil {
		return nil, s.failOrRetry(ctx, pending, scheduled, err)
	}
	slog.Info("serverupdate: binaries replaced", "version", pending.Version, "paths", rec.Binaries)
	if scheduled && !s.busyNow(ctx).Idle() {
		if err := s.unswap(rec); err != nil {
			return nil, s.fail(ctx, pending, fmt.Errorf("a run started during the download, and undoing the swap failed: %w", err))
		}
		s.release()
		s.publish(ctx, pending.RequestedBy, events.ServerUpdateScheduled, pending.Version, errBecameBusy.Error())
		return nil, errBecameBusy
	}
	if err := s.cfg.Store.SetPendingServerUpdate(ctx, nil); err != nil {
		slog.Warn("serverupdate: clear the applied pending update", "error", err)
	}
	// The restart runs after the RPC response has been written, by which
	// point the request context may be on its way out; the restarting
	// phase still has to reach the feed, so it is detached from it.
	restartCtx := context.WithoutCancel(ctx)
	return func() { s.restart(restartCtx, pending, rec) }, nil
}

// errBecameBusy is a scheduled update undone because the server stopped
// being idle while the release downloaded.
var errBecameBusy = errors.New("a run started while the release downloaded; the update applies at the next idle moment")

// retryable marks a failure of the network or the release host, which a
// scheduled update retries at a later idle poll.
type retryable struct{ error }

func (r retryable) Unwrap() error { return r.error }

// failOrRetry records err, except that a scheduled update whose failure is
// retryable stays pending for the next idle poll, up to maxRetries times.
func (s *Service) failOrRetry(ctx context.Context, pending store.PendingServerUpdate, scheduled bool, err error) error {
	if scheduled && errors.As(err, new(retryable)) {
		s.mu.Lock()
		if s.retrying.Version != pending.Version || !s.retrying.RequestedAt.Equal(pending.RequestedAt) {
			s.retrying, s.retries = pending, 0
		}
		s.retries++
		again := s.retries < maxRetries
		s.mu.Unlock()
		if again {
			slog.Warn("serverupdate: scheduled update failed; retrying at the next idle poll", "version", pending.Version, "error", err)
			s.release()
			return err
		}
	}
	return s.fail(ctx, pending, err)
}

// swap keeps each binary as <path>.prev, installs the release, and writes
// the swap record the next start reads. Before the first rename the record
// says swapping, so a start after a crash mid-swap puts the previous
// binaries back. When the release migrates the database the record names
// the snapshot the restart takes. A failure restores whatever it replaced.
func (s *Service) swap(ctx context.Context, pending store.PendingServerUpdate, m selfupdate.Manifest) (*swapRecord, error) {
	rec := &swapRecord{
		Phase:       recordSwapping,
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
		if err = os.Remove(snap); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove the last database snapshot: %w", err)
		}
		rec.Database, rec.Snapshot, rec.SchemaBefore = s.cfg.Database, snap, schema
	}
	if err = keepPrevious(rec.Binaries); err != nil {
		return nil, err
	}
	if err = saveRecord(s.cfg.StateDir, rec); err != nil {
		return nil, err
	}
	replaced, err := selfupdate.UpdateBinaries(ctx,
		s.cfg.Checker.BaseURL(), pending.Version, s.self, "aether-server", "aether")
	if err != nil {
		if rerr := restorePrevious(replaced); rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		return nil, errors.Join(retryable{fmt.Errorf("download failed: %w", err)}, removeRecord(s.cfg.StateDir))
	}
	rec.Phase = recordVerifying
	if err := saveRecord(s.cfg.StateDir, rec); err != nil {
		return nil, errors.Join(err, s.unswap(rec))
	}
	return rec, nil
}

// unswap puts the previous binaries back and drops the swap record.
func (s *Service) unswap(rec *swapRecord) error {
	if err := restorePrevious(rec.Binaries); err != nil {
		return err
	}
	return removeRecord(s.cfg.StateDir)
}

func removeRecord(dir string) error {
	if err := os.Remove(recordPath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("serverupdate: remove %s: %w", recordPath(dir), err)
	}
	return nil
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
	if err := takeSnapshot(rec); err != nil {
		s.undo(ctx, pending, rec, err)
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
// the new one. Nothing migrated the database, so any snapshot is dropped.
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
