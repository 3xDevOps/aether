package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store"
)

// Tick is the idle poll: the scheduler calls it once per poll interval
// with whether the server is idle right now. A pending update applies the
// first time idle is true, and applying it does not return - the process
// re-executes on the new binary.
func (s *Service) Tick(ctx context.Context, idle bool) {
	if !idle {
		return
	}
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	if err != nil {
		slog.Warn("serverupdate: read pending update", "error", err)
		return
	}
	if state.Pending == nil {
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

// apply downloads and swaps the release, recording the outcome either way.
// It returns the restart, which re-executes the server and never returns
// on success; a failure leaves the running binary untouched, clears the
// pending row so the next idle tick does not retry a broken tag, and is
// reported as `last` on server.update_status.
func (s *Service) apply(ctx context.Context, pending store.PendingServerUpdate) (func(), error) {
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
	return func() { s.restart(ctx, pending) }, nil
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
	err := s.cfg.Exec(s.self, argv, os.Environ())
	// Exec only returns on failure. Under systemd the unit can still be
	// restarted from outside; anywhere else there is nothing left to try,
	// and the swapped binary takes effect at the next start.
	if os.Getenv("INVOCATION_ID") != "" {
		slog.Error("serverupdate: re-exec failed, asking systemd to restart", "error", err)
		if rerr := s.cfg.Restart(); rerr != nil {
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

// systemctlRestart is the fallback restart when re-exec fails under a
// systemd unit.
func systemctlRestart() error {
	out, err := exec.Command("systemctl", "restart", "aether-server").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart aether-server: %w: %s", err, out)
	}
	return nil
}
