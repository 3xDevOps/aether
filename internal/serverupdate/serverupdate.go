// Package serverupdate replaces the running aether-server binary with a
// published release and restarts onto it, on an admin's request. The
// client has no shell on the server box, so this is the only way an admin
// can move the server forward from a laptop; the server already runs as
// root under the shipped unit (docs/install.md), so it can do the swap
// itself.
//
// The release feed is the pinned GitHub repository in internal/selfupdate
// and nothing else: the client names a tag, never a URL. Every binary is
// downloaded and checksum-verified before any of them is replaced, and
// each is then renamed into place, so a bad tag, a network error, or a
// checksum mismatch leaves every binary as it was.
//
// Everything this package does to the machine it runs on goes through
// Config.Host, which only cmd/aether-server supplies. A service built
// without one still answers server.update_status; it just refuses to
// apply anything.
package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// ErrIncapable is returned when this server process cannot update itself.
// The wrapped message says why - most often the documented unprivileged
// install, where the binary directory belongs to root and the service user
// only reads it. The admin runs ManualCommands on the server host instead.
var ErrIncapable = errors.New("this server cannot update itself")

// notWritable explains the unprivileged install, the common reason.
const notWritable = "its binary directory is not writable by the server process"

// noHost explains a service built without host controls, which is every
// build but the real server command.
const noHost = "this build has no host restart mechanics"

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

// Config wires the service. Store and Bus are required; everything that
// touches the host goes through Host, which is never defaulted.
type Config struct {
	Store   store.Store
	Bus     events.Bus
	Checker *selfupdate.Checker
	// Host carries the process-replacing side effects. A zero Host leaves
	// the service unable to apply anything, which is what keeps every test
	// binary away from the host's systemd. cmd/aether-server passes
	// HostProcess().
	Host Host
	// Executable is the running aether-server binary. Empty resolves
	// os.Executable.
	Executable string
	// Busy reports what the server is doing, so a scheduled update lands
	// at the first idle moment and an admin can see what it is waiting
	// for. Nil reports an idle server: a deployment with no run engine has
	// nothing to wait for.
	Busy func(context.Context) domain.ServerBusy
	// Now reads the clock.
	Now func() time.Time
}

// Service applies server self-updates. One at a time: the RPC path and the
// idle poll both take applying before touching a binary.
type Service struct {
	cfg  Config
	self string
	// incapable is why this process cannot update itself, fixed at
	// construction and empty when it can. It covers the reasons that
	// cannot change while the process runs - a binary that could not be
	// resolved, a build with no host controls - as opposed to the
	// directory permissions, which are probed per call.
	incapable string

	mu       sync.Mutex
	applying bool
}

// New builds the service. It never fails: self-update is a convenience,
// and a server that cannot resolve its own binary must still serve runs.
// A problem found here is reported through Capable and
// server.update_status instead, where an admin will actually read it.
//
// Store and Bus are the two things the caller must get right, so those
// stay errors - they are wiring bugs, not deployment facts.
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
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Service{cfg: cfg}
	if !cfg.Host.complete() {
		s.incapable = noHost
	}
	self := cfg.Executable
	if self == "" {
		found, err := os.Executable()
		if err != nil {
			return s.disabled(fmt.Sprintf("locating this binary failed: %v", err)), nil
		}
		self = found
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return s.disabled(fmt.Sprintf("resolving %s failed: %v", self, err)), nil
	}
	s.self = resolved
	return s, nil
}

// disabled records why the service cannot apply anything and logs it once,
// at construction, so the reason is in the journal as well as on the wire.
func (s *Service) disabled(reason string) *Service {
	s.incapable = reason
	slog.Warn("serverupdate: this server cannot update itself", "reason", reason)
	return s
}

// Capable reports whether this process can replace its own binary and
// restart onto it. The directory permissions are probed rather than
// assumed: an install can be moved to an unprivileged unit without the
// server being rebuilt.
func (s *Service) Capable() bool { return s.incapableReason() == "" }

// incapableReason is why this server cannot update itself, empty when it
// can.
func (s *Service) incapableReason() string {
	if s.incapable != "" {
		return s.incapable
	}
	if err := selfupdate.CheckWritable(filepath.Dir(s.self)); err != nil {
		return notWritable
	}
	return ""
}

// Status reports the server's own update state. A release check that
// fails - an air-gapped box, GitHub down - is not an error here: the
// pending update, the last outcome, and the capability still answer.
func (s *Service) Status(ctx context.Context) (protocol.ServerUpdateStatusResult, error) {
	out := protocol.ServerUpdateStatusResult{ServerVersion: version.Version}
	if reason := s.incapableReason(); reason != "" {
		out.Incapable = reason
		out.ManualCommands = ManualCommands()
	} else {
		out.Capable = true
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
		// Asked live rather than cached from the last poll: a status call
		// an admin makes after killing the last run should say the update
		// is about to land, not what was true 30 seconds ago. Nothing
		// pending means this is never reached, so an idle deployment
		// never pays for the run-table scan.
		if busy := s.busyNow(ctx); !busy.Idle() {
			out.Waiting = &protocol.ServerUpdateWaiting{
				Runs: busy.Runs, Paused: busy.Paused, Shells: busy.Shells,
			}
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
	if reason := s.incapableReason(); reason != "" {
		return protocol.ServerUpdateResult{}, nil, fmt.Errorf("%w: %s", ErrIncapable, reason)
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
