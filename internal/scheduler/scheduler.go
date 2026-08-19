// Package scheduler owns the run lifecycle. It provisions run containers
// (checkout via the GitEngine seam, container via runtime.Runtime, agent
// PTY via the PTYHost seam), enforces the legal status transitions,
// supervises the agent process to exit, detects stalls, recovers
// supervision after server reboots, garbage-collects expired checkouts,
// and refuses new runs below the free-space floor. It is the single writer
// of run statuses and the sole publisher of run.status events. What each
// of those guards promises, and how they are tuned:
// docs/failure-handling.md.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// ErrInvalidTransition is returned when a steering call or lifecycle step
// would move a run through an illegal status transition.
var ErrInvalidTransition = errors.New("scheduler: invalid run state transition")

// ErrDiskFull is returned when a new run would start with less free space
// than Config.MinFreeBytes. It is a refusal, not a failure: nothing is
// created, and the runs already on the disk keep going.
var ErrDiskFull = errors.New("scheduler: not enough free disk space to start a new run")

// DefaultMinFreeBytes is the shipped free-space floor: 1 GiB of headroom
// for the checkout, the container's writes and the event log a new run is
// about to produce.
const DefaultMinFreeBytes = 1 << 30

// Config wires the scheduler's dependencies and tuning knobs.
type Config struct {
	Store          store.Store
	Runtime        runtime.Runtime
	Bus            events.Bus
	Git            GitEngine
	PTY            PTYHost
	StateDir       string // <data>/scheduler
	HomesDir       string // <data>/homes; empty disables credential-home mounts
	ProfilesDir    string // <data>/profiles; default sibling of HomesDir
	Profiles       profileService
	ReposDir       string        // <data>/repos; required only for non-root run users (ownership pass)
	WorktreeMount  string        // default "/workspace"
	StallThreshold time.Duration // default 10m
	PollInterval   time.Duration // default 30s
	StopGrace      time.Duration // default 10s
	CheckoutTTL    time.Duration // default 72h; negative disables GC
	// MinFreeBytes is the free-space floor: a launch or relaunch that
	// would start below it is refused with ErrDiskFull rather than filling
	// the disk out from under the runs already on it. Runs already
	// provisioned are never touched - the branch is the artifact and a
	// half-written checkout is worse than a refused one. Zero applies
	// DefaultMinFreeBytes; negative disables the floor.
	MinFreeBytes int64
	// Harnesses overrides or extends the shipped harness registry
	// (internal/harness: claude, codex, aider, opencode, custom); "fake"
	// (the deterministic e2e agent) is registered here by default. An
	// override replaces the registry argv but keeps the registry
	// profile's credential paths, env passthrough, and user mapping -
	// this is also how a deployment supplies the "custom" command. The
	// registry's MCP registration and resume flags belong to the CLI it
	// ships with, so an overridden harness never has either appended: its
	// conflict coordination degrades to the overlap notice, and a relaunch
	// starts the agent fresh.
	Harnesses map[string]HarnessSpec
}

// HarnessSpec is one agent harness's argv templates.
type HarnessSpec struct {
	TUIArgs      []string // argv template; "{task}" placeholder substituted
	HeadlessArgs []string
}

// fakeAgentEnv names the environment variable the "fake" harness reads its
// argv from (whitespace-split) when no explicit spec overrides it.
const fakeAgentEnv = "AETHER_FAKE_AGENT"

func defaultHarnesses() map[string]HarnessSpec {
	return map[string]HarnessSpec{
		// The deterministic e2e agent: argv comes from AETHER_FAKE_AGENT
		// at launch time. Every other shipped harness lives in
		// internal/harness.
		"fake": {},
	}
}

// Scheduler is the run lifecycle engine. Its exported method set satisfies
// the sshd.RunController seam.
type Scheduler struct {
	cfg       Config
	harnesses map[string]HarnessSpec

	// superCtx bounds every supervision goroutine; Close (and Start's ctx
	// ending) cancels it. Containers are never stopped by cancellation.
	superCtx    context.Context
	superCancel context.CancelFunc
	wg          sync.WaitGroup

	mu              sync.Mutex
	runs            map[domain.RunID]*supervised
	credentialUsers map[*credentialUserReservation]struct{}
	// coordination is the attached conflict-coordination service and the
	// staged-bridge directory (UseCoordination); nil means new containers
	// get no coordination assets.
	coordination *coordination
}

// credentialUserReservation protects one writable member+harness
// credential home from ownership changes while its container is live.
// Root containers do not need a reservation because they skip chown.
type credentialUserReservation struct {
	memberID domain.MemberID
	harness  string
	user     string
	owner    string
	run      *supervised
}

// supervised is the in-memory state of one run with a live container.
type supervised struct {
	runID       domain.RunID
	sessionID   domain.SessionID
	workspaceID domain.WorkspaceID
	containerID runtime.ID
	task        string
	// memberID and harness identify the credential home
	// (<homes>/<member>/<harness>) the run's mounts share with every
	// other live run of the same pair.
	memberID domain.MemberID
	harness  string

	// Mutated only under Scheduler.mu.
	status        domain.RunStatus
	startedAt     time.Time
	paused        bool
	killRequested bool
	killActor     domain.MemberID
	// runUser is the resolved numeric "uid:gid" the run's container and
	// ownership pass use; empty means root (no ownership pass). Set once
	// the user is resolved during provisioning, or from the sidecar on
	// recovery.
	runUser         string
	userReservation *credentialUserReservation
	// exitObserved / exitCode are the durable Wait result, persisted
	// before finalize so a crash can resume the original exit.
	exitObserved bool
	exitCode     int
	// The coordination assets mounted into this run's container, mirrored
	// into the sidecar before the container is created (coordination.go).
	bridgeDigest string
	bridgePath   string
	coordDir     string
}

// New validates cfg, applies defaults, and prepares the state directory.
func New(cfg Config) (*Scheduler, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("scheduler: config requires a Store")
	case cfg.Runtime == nil:
		return nil, errors.New("scheduler: config requires a Runtime")
	case cfg.Bus == nil:
		return nil, errors.New("scheduler: config requires a Bus")
	case cfg.Git == nil:
		return nil, errors.New("scheduler: config requires a GitEngine")
	case cfg.PTY == nil:
		return nil, errors.New("scheduler: config requires a PTYHost")
	case cfg.StateDir == "":
		return nil, errors.New("scheduler: config requires a StateDir")
	}
	if cfg.WorktreeMount == "" {
		cfg.WorktreeMount = "/workspace"
	}
	if cfg.StallThreshold <= 0 {
		cfg.StallThreshold = 10 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = 10 * time.Second
	}
	if cfg.CheckoutTTL == 0 {
		cfg.CheckoutTTL = 72 * time.Hour
	}
	if cfg.MinFreeBytes == 0 {
		cfg.MinFreeBytes = DefaultMinFreeBytes
	}
	harnesses := defaultHarnesses()
	maps.Copy(harnesses, cfg.Harnesses)
	if cfg.HomesDir != "" && cfg.ProfilesDir == "" {
		cfg.ProfilesDir = filepath.Join(filepath.Dir(cfg.HomesDir), "profiles")
	}
	if cfg.Profiles == nil && cfg.HomesDir != "" {
		if db, ok := cfg.Store.(*store.DB); ok {
			svc, err := profile.New(db, cfg.ProfilesDir)
			if err != nil {
				return nil, fmt.Errorf("scheduler: profile service: %w", err)
			}
			cfg.Profiles = svc
		}
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return nil, fmt.Errorf("scheduler: create state dir: %w", err)
	}
	if cfg.ProfilesDir != "" {
		if err := os.MkdirAll(filepath.Join(cfg.ProfilesDir, "runs"), 0o755); err != nil {
			return nil, fmt.Errorf("scheduler: create profiles dir: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		cfg:             cfg,
		harnesses:       harnesses,
		superCtx:        ctx,
		superCancel:     cancel,
		runs:            make(map[domain.RunID]*supervised),
		credentialUsers: make(map[*credentialUserReservation]struct{}),
	}, nil
}

// Start performs reboot recovery, then drives the stall-detection and
// checkout-GC loops until ctx is done or Close is called. Shutting down
// never stops containers; supervision simply ends.
func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.recoverRuns(ctx); err != nil {
		return err
	}
	stall := time.NewTicker(s.cfg.PollInterval)
	defer stall.Stop()
	var gcC <-chan time.Time
	if s.cfg.CheckoutTTL > 0 {
		s.sweepCheckouts(ctx)
		gc := time.NewTicker(time.Hour)
		defer gc.Stop()
		gcC = gc.C
	}
	for {
		select {
		case <-ctx.Done():
			s.superCancel()
			return nil
		case <-s.superCtx.Done():
			return nil
		case <-stall.C:
			s.checkStalls(ctx)
		case <-gcC:
			s.sweepCheckouts(ctx)
		}
	}
}

// Close stops supervision and the Start loops. Containers keep running.
func (s *Scheduler) Close() error {
	s.superCancel()
	s.wg.Wait()
	return nil
}

// command resolves the container argv and launch profile for a
// harness/mode pair, with "{task}" substituted. Config.Harnesses argv
// overrides win over the shipped registry templates; the registry profile
// (credential paths, env passthrough, user mapping) applies either way.
func (s *Scheduler) command(harnessName string, mode domain.LaunchMode, task string) ([]string, harness.Profile, error) {
	profile, inRegistry := harness.Lookup(harnessName)
	var tui, headless []string
	switch spec, ok := s.harnesses[harnessName]; {
	case ok:
		tui, headless = spec.TUIArgs, spec.HeadlessArgs
		// An explicit argv override is respected verbatim: the registry's
		// MCP and resume flags are for the CLI it ships with, and nothing
		// checks the override still is that CLI.
		profile.MCPConfigFlag = ""
		profile.ResumeFlag = ""
	case inRegistry:
		tui, headless = profile.TUIArgs, profile.HeadlessArgs
	default:
		return nil, harness.Profile{}, fmt.Errorf("scheduler: unknown harness %q", harnessName)
	}
	var argv []string
	switch mode {
	case domain.LaunchTUI:
		argv = tui
	case domain.LaunchHeadless:
		argv = headless
	default:
		return nil, harness.Profile{}, fmt.Errorf("scheduler: invalid launch mode %q", mode)
	}
	if harnessName == "fake" && len(argv) == 0 {
		argv = strings.Fields(os.Getenv(fakeAgentEnv))
	}
	if len(argv) == 0 {
		return nil, harness.Profile{}, fmt.Errorf("scheduler: harness %q has no command for mode %q", harnessName, mode)
	}
	return harness.Argv(argv, task), profile, nil
}

// containerSpec builds the runtime spec for a provisioned run (§6.1 plus
// the Wave 2 amendments: the container's main process is the agent, on a
// TTY, in both modes; the agent commits under the owning member's git
// identity; credential homes ride as additional mounts; the run ID is the
// creation key recovery uses to find a container whose ID never reached
// the sidecar).
func (s *Scheduler) containerSpec(run *domain.Run, ws *domain.Workspace, member *domain.Member, argv []string, profile harness.Profile, mounts []runtime.Mount, user string) runtime.Spec {
	env := make(map[string]string, len(ws.Env)+len(profile.EnvPassthrough)+7)
	for _, k := range profile.EnvPassthrough {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			env[k] = v
		}
	}
	maps.Copy(env, ws.Env)
	env["TERM"] = "xterm-256color"
	// HOME follows the resolved run user so harnesses read and write
	// their config and login state where the credential mounts land.
	env["HOME"] = harness.HomeDir(user)
	env["AETHER_RUN_ID"] = string(run.ID)
	env["AETHER_SESSION_ID"] = string(run.SessionID)
	env["GIT_AUTHOR_NAME"] = member.DisplayName
	env["GIT_COMMITTER_NAME"] = member.DisplayName
	env["GIT_AUTHOR_EMAIL"] = string(member.ID) + "@aether.local"
	env["GIT_COMMITTER_EMAIL"] = string(member.ID) + "@aether.local"
	return runtime.Spec{
		Name:              string(run.ID),
		Image:             ws.Image,
		Env:               env,
		SetupScript:       ws.SetupScript,
		WorktreeHostPath:  run.Worktree,
		WorktreeMountPath: s.cfg.WorktreeMount,
		WorkingDir:        s.cfg.WorktreeMount,
		Command:           argv,
		TTY:               true,
		Mounts:            mounts,
		User:              user,
		CreationKey:       string(run.ID),
	}
}

// taskLine reduces a task to its commit-message form: first line only,
// truncated to 72 characters.
func taskLine(task string) string {
	if i := strings.IndexAny(task, "\r\n"); i >= 0 {
		task = task[:i]
	}
	if r := []rune(task); len(r) > 72 {
		task = string(r[:72])
	}
	return task
}
