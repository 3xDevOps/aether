// Package serverupdate replaces the running aether-server binary with a
// published release and restarts onto it, on an admin's request. The
// client has no shell on the server box, so this is the only way an admin
// can move the server forward from a laptop; the server already runs as
// root under the shipped unit (docs/install.md), so it can do the swap
// itself.
//
// The release feed is the pinned GitHub repository in internal/selfupdate
// and nothing else: the client names a tag, never a URL. Downloads are
// checksum-verified and swapped in atomically by selfupdate.Apply, so a
// failure at any point leaves the running binary untouched.
package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// ErrIncapable is returned when the server process cannot replace its own
// binary: the documented unprivileged install, where the binary directory
// belongs to root and the service user only reads it. The admin runs
// ManualCommands on the server host instead.
var ErrIncapable = errors.New("this server cannot update itself: its binary directory is not writable by the server process")

// ErrBadTag is returned for a version that is not a release tag.
var ErrBadTag = errors.New("version must be a release tag: v plus semver, for example v0.2.0")

// ErrBadWhen is returned for an unknown when value.
var ErrBadWhen = errors.New(`when must be "now", "idle", or "cancel"`)

// ErrBusy is returned when an update is already being applied.
var ErrBusy = errors.New("a server update is already being applied")

// ManualCommands is what an admin runs on the server host when the server
// cannot update itself.
func ManualCommands() []string {
	return []string{"sudo aether update", "sudo systemctl restart aether-server"}
}

// Config wires the service. Executable, Exec, Restart, and Now default to
// the real thing; tests inject all four so no test ever touches a real
// binary or re-executes the test process.
type Config struct {
	Store   store.Store
	Bus     events.Bus
	Checker *selfupdate.Checker
	// Executable is the running aether-server binary. Empty resolves
	// os.Executable.
	Executable string
	// Exec replaces this process image with the binary at path. It does
	// not return on success. Empty uses syscall.Exec.
	Exec func(path string, argv, env []string) error
	// Restart is the fallback when Exec fails under systemd. Empty runs
	// `systemctl restart aether-server`.
	Restart func() error
	// Now reads the clock.
	Now func() time.Time
}

// Service applies server self-updates. One at a time: the RPC path and the
// idle poll both take applying before touching a binary.
type Service struct {
	cfg  Config
	self string

	mu       sync.Mutex
	applying bool
}

// New validates cfg and resolves the binary to replace.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("serverupdate: config requires a Store")
	case cfg.Bus == nil:
		return nil, errors.New("serverupdate: config requires a Bus")
	}
	if cfg.Checker == nil {
		cfg.Checker = selfupdate.DefaultChecker()
	}
	if cfg.Exec == nil {
		cfg.Exec = syscall.Exec
	}
	if cfg.Restart == nil {
		cfg.Restart = systemctlRestart
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	self := cfg.Executable
	if self == "" {
		found, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("serverupdate: locate this binary: %w", err)
		}
		self = found
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return nil, fmt.Errorf("serverupdate: resolve %s: %w", self, err)
	}
	return &Service{cfg: cfg, self: resolved}, nil
}

// Capable reports whether this process can replace its own binary. It is
// probed rather than assumed: an install can be moved to an unprivileged
// unit without the server being rebuilt.
func (s *Service) Capable() bool {
	return selfupdate.CheckWritable(filepath.Dir(s.self)) == nil
}

// Status reports the server's own update state. A release check that
// fails - an air-gapped box, GitHub down - is not an error here: the
// pending update, the last outcome, and the capability still answer.
func (s *Service) Status(ctx context.Context) (protocol.ServerUpdateStatusResult, error) {
	out := protocol.ServerUpdateStatusResult{
		ServerVersion: version.Version,
		Capable:       s.Capable(),
	}
	if !out.Capable {
		out.ManualCommands = ManualCommands()
	}
	if check, err := s.cfg.Checker.Check(ctx); err == nil {
		out.Latest = check.Latest
		out.UpdateAvailable = check.UpdateAvailable
	} else {
		slog.Warn("serverupdate: release check failed", "error", err)
	}
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	if err != nil {
		return protocol.ServerUpdateStatusResult{}, err
	}
	if p := state.Pending; p != nil {
		out.Pending = &protocol.PendingServerUpdate{
			Version:     p.Version,
			RequestedBy: string(p.RequestedBy),
			RequestedAt: p.RequestedAt.UTC().Format(time.RFC3339),
		}
	}
	if l := state.Last; l != nil {
		out.Last = &protocol.ServerUpdateAttempt{
			Version: l.Version,
			Outcome: l.Outcome,
			Detail:  l.Detail,
			At:      l.At.UTC().Format(time.RFC3339),
		}
	}
	return out, nil
}

// Update handles one server.update call. For "now" the release is applied
// before this returns, so the result reports what actually happened; the
// returned restart function re-executes the server and never returns, so
// the caller runs it only after the response has reached the client.
// restart is nil for every other outcome.
func (s *Service) Update(ctx context.Context, actor domain.MemberID, p protocol.ServerUpdateParams) (protocol.ServerUpdateResult, func(), error) {
	switch p.When {
	case protocol.ServerUpdateCancel:
		res, err := s.cancel(ctx, actor)
		return res, nil, err
	case protocol.ServerUpdateNow, protocol.ServerUpdateIdle:
	default:
		return protocol.ServerUpdateResult{}, nil, ErrBadWhen
	}
	if !s.Capable() {
		return protocol.ServerUpdateResult{}, nil, ErrIncapable
	}
	tag, err := s.resolveTag(ctx, p.Version)
	if err != nil {
		return protocol.ServerUpdateResult{}, nil, err
	}
	pending := store.PendingServerUpdate{Version: tag, RequestedBy: actor, RequestedAt: s.cfg.Now().UTC()}
	res := protocol.ServerUpdateResult{
		Version:     tag,
		RequestedBy: string(actor),
		RequestedAt: pending.RequestedAt.Format(time.RFC3339),
	}
	if p.When == protocol.ServerUpdateIdle {
		// One pending update at a time: a second request replaces the
		// first rather than queueing behind it.
		if serr := s.cfg.Store.SetPendingServerUpdate(ctx, &pending); serr != nil {
			return protocol.ServerUpdateResult{}, nil, serr
		}
		res.Status = protocol.ServerUpdateScheduled
		s.publish(ctx, actor, events.ServerUpdateScheduled, tag, "")
		return res, nil, nil
	}
	restart, err := s.apply(ctx, pending)
	if err != nil {
		return protocol.ServerUpdateResult{}, nil, err
	}
	res.Status = protocol.ServerUpdateApplying
	return res, restart, nil
}

// cancel clears the pending update. Cancelling nothing is not an error:
// the caller asked for no pending update and there is none.
func (s *Service) cancel(ctx context.Context, actor domain.MemberID) (protocol.ServerUpdateResult, error) {
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	if err != nil {
		return protocol.ServerUpdateResult{}, err
	}
	if err := s.cfg.Store.SetPendingServerUpdate(ctx, nil); err != nil {
		return protocol.ServerUpdateResult{}, err
	}
	out := protocol.ServerUpdateResult{Status: protocol.ServerUpdateCancelled}
	if state.Pending != nil {
		out.Version = state.Pending.Version
		s.publish(ctx, actor, events.ServerUpdateCancelled, state.Pending.Version, "")
	}
	return out, nil
}

// resolveTag validates a client-supplied tag or asks the release feed for
// the newest one. The tag only ever names a release in the pinned
// repository; a URL from the client is never followed.
func (s *Service) resolveTag(ctx context.Context, tag string) (string, error) {
	if tag != "" {
		if !selfupdate.ValidTag(tag) {
			return "", fmt.Errorf("%w (got %q)", ErrBadTag, tag)
		}
		return tag, nil
	}
	latest, err := selfupdate.LatestTag(ctx, s.cfg.Checker.BaseURL()+"/releases/latest")
	if err != nil {
		return "", err
	}
	return latest, nil
}
