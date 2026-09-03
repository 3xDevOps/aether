package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store"
)

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
	restart, err := s.apply(ctx, *state.Pending)
	if err != nil {
		if !errors.Is(err, ErrBusy) {
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

// apply downloads and swaps the release, recording the outcome either way.
// It returns the restart, which re-executes the server and never returns
// on success; a failure leaves the running binary untouched, clears the
// pending row so the next idle tick does not retry a broken tag, and is
// reported as `last` on server.update_status.
func (s *Service) apply(ctx context.Context, pending store.PendingServerUpdate) (func(), error) {
	// The invariant every caller depends on, checked where it is used
	// rather than at each call site: nothing downloads a release or
	// replaces a binary on a server that cannot restart onto it.
	if reason := s.incapableReason(); reason != "" {
		return nil, fmt.Errorf("%w: %s", ErrIncapable, reason)
	}
	if !s.claim() {
		return nil, ErrBusy
	}
	s.publish(ctx, pending.RequestedBy, events.ServerUpdateApplying, pending.Version, "")
	replaced, err := selfupdate.UpdateBinaries(ctx,
		s.cfg.Checker.BaseURL(), pending.Version, s.self, "aether-server", "aether")
	if err != nil {
		s.release()
		s.record(ctx, pending.Version, store.ServerUpdateFailed, err.Error())
		s.publish(ctx, pending.RequestedBy, events.ServerUpdateFailed, pending.Version, err.Error())
		return nil, err
	}
	slog.Info("serverupdate: binaries replaced", "version", pending.Version, "paths", replaced)
	s.record(ctx, pending.Version, store.ServerUpdateApplied, "")
	// The restart runs after the RPC response has been written, by which
	// point the request context may be on its way out; the restarting
	// phase still has to reach the feed, so it is detached from it.
	restartCtx := context.WithoutCancel(ctx)
	return func() { s.restart(restartCtx, pending) }, nil
}

// claim takes the one applying slot, so the RPC path and the idle poll can
// never download over each other.
func (s *Service) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applying {
		return false
	}
	s.applying = true
	return true
}

func (s *Service) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applying = false
}

// restart re-executes the new binary in place. syscall.Exec keeps the PID,
// which is what the shipped `Restart=on-failure` unit needs - a clean exit
// would not be restarted - and is also what makes a foreground
// `aether-server serve` come back. Runs survive because the scheduler
// reattaches to live containers on boot; attached terminals and live syncs
// drop and reconnect.
func (s *Service) restart(ctx context.Context, pending store.PendingServerUpdate) {
	s.publish(ctx, pending.RequestedBy, events.ServerUpdateRestarting, pending.Version, "")
	argv := append([]string{s.self}, os.Args[1:]...)
	err := s.cfg.Host.Exec(s.self, argv, os.Environ())
	if err == nil {
		// syscall.Exec never returns on success, so reaching here at all
		// means the process was not replaced, whatever the seam reported.
		err = errors.New("returned without replacing the process")
	}
	// Exec only returns on failure. Under systemd the unit can still be
	// restarted from outside; anywhere else there is nothing left to try,
	// and the swapped binary takes effect at the next start.
	if s.cfg.Host.UnderSystemd() {
		slog.Error("serverupdate: re-exec failed, asking systemd to restart", "error", err)
		if rerr := s.cfg.Host.Restart(); rerr != nil {
			s.failRestart(ctx, pending, fmt.Errorf("re-exec failed (%v) and systemctl restart failed: %w", err, rerr))
		}
		return
	}
	s.failRestart(ctx, pending, fmt.Errorf("re-exec %s: %w", s.self, err))
}

// failRestart reports a swapped binary the server could not restart into.
// The new binary is on disk and the next start picks it up, so the detail
// says exactly that rather than pretending nothing happened.
func (s *Service) failRestart(ctx context.Context, pending store.PendingServerUpdate, cause error) {
	s.release()
	detail := cause.Error() + "; " + pending.Version + " is installed and starts on the next `systemctl restart aether-server`"
	s.record(ctx, pending.Version, store.ServerUpdateFailed, detail)
	s.publish(ctx, pending.RequestedBy, events.ServerUpdateFailed, pending.Version, detail)
}

// record stores the attempt's outcome, which also clears the pending row.
func (s *Service) record(ctx context.Context, version, outcome, detail string) {
	if err := s.cfg.Store.SetLastServerUpdate(ctx, store.ServerUpdateAttempt{
		Version: version,
		Outcome: outcome,
		Detail:  detail,
		At:      s.cfg.Now().UTC(),
	}); err != nil {
		slog.Error("serverupdate: record update outcome", "outcome", outcome, "error", err)
	}
}

// publish stamps one phase into every workspace's timeline. A server
// update touches every workspace, and the timeline is per-workspace, so
// the act is attributed everywhere it is felt. A server with no workspaces
// yet has nowhere to record it; the RPC result still reports the phase.
func (s *Service) publish(ctx context.Context, actor domain.MemberID, phase events.ServerUpdatePhase, version, detail string) {
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("serverupdate: list workspaces for the update feed", "error", err)
		return
	}
	payload := events.ServerUpdatePayload{Phase: phase, Version: version, ActorID: actor, Detail: detail}
	for _, ws := range workspaces {
		if _, perr := s.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: ws.ID,
			ActorID:     actor,
			Payload:     payload,
		}); perr != nil {
			slog.Warn("serverupdate: publish update phase", "phase", phase, "workspace", ws.ID, "error", perr)
		}
	}
}
