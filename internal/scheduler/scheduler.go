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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
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
	Store         store.Store
	Runtime       runtime.Runtime
	Bus           events.Bus
	Git           GitEngine
	PTY           PTYHost
	StateDir      string
	Homes         *memberhome.Manager
	Profiles      profileService
	ReposDir      string
	WorktreeMount string
	StandardImage string
	StallThreshold time.Duration
	PollInterval   time.Duration
	StopGrace      time.Duration // default 10s
	CheckoutTTL    time.Duration // default 72h; negative disables GC
	// MinFreeBytes is the free-space floor: a launch or relaunch that
	// would start below it is refused with ErrDiskFull rather than filling
	// the disk out from under the runs already on it. Runs already
	// provisioned are never touched - the branch is the artifact and a
	// half-written checkout is worse than a refused one.
	MinFreeBytes int64
	// Harnesses overrides or extends the shipped harness registry
	// (internal/harness: claude, codex, pi, amp, opencode, custom); "fake"
	// (the deterministic e2e agent) is registered here by default. An
	// override replaces the registry argv but retains the profile's user,
	// environment passthrough, resume, and coordination settings. Member
	// definitions shape argv inside that member's own container and do not
	// leak across members.
	Harnesses map[string]HarnessSpec
	// ServerBinary is the server binary staged into run containers to
	// serve the MCP bridge (docs/mcp-bridge.md). Empty means
	// DefaultServerBinary: the running binary, which survives a PATH
	// change, a relative launch, and an upgrade that replaced the file
	// underneath the process. The E2E suite points it at a binary it
	// built, because under `go test` /proc/self/exe is the test binary and
	// has no mcp subcommand.
	ServerBinary string
}

// DefaultServerBinary is the running server binary, /proc/self/exe rather
// than os.Args[0].
const DefaultServerBinary = "/proc/self/exe"

// HarnessSpec is an administrator-supplied generic harness definition. A
// zero-valued Executable keeps the legacy argv-only override behavior for
// shipped profiles and the deterministic fake harness.
type HarnessSpec struct {
	TUIArgs         []string
	HeadlessArgs    []string
	Executable      string
	ProfileRoot     string
	CredentialPaths []string
	DenyNames       []string
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

	mu   sync.Mutex
	runs map[domain.RunID]*supervised
	// pending marks runs whose row exists but whose checkout/provisioning
	// handoff has not reached runs yet. Delete waits for this short window so
	// it cannot remove a row while its checkout is still being created; Kill
	// records its request for the handoff to transfer into supervision.
	pending map[domain.RunID]*pendingRun
	// runShellLocks serializes shell-tab creation per run so the tab cap
	// cannot be raced past; a hung exec on one run never blocks another.
	// Entries are created on first use and kept for the scheduler's life.
	runShellLocks   map[domain.RunID]*sync.Mutex
	terminalLocks   map[domain.MemberID]*sync.Mutex
	terminals       map[domain.MemberID]*terminalSupervision
	credentialUsers map[*credentialUserReservation]struct{}
	titleMu       sync.Mutex
	titleUpdates  map[domain.RunID]*pendingRunTitle
	// coordination is the attached conflict-coordination service and the
	// staged-bridge directory (UseCoordination); nil means new containers
	// get no coordination assets.
	coordination *coordination
	// updates is the attached server self-update service (UseUpdates);
	// nil means a scheduled update never applies.
	updates UpdateTicker
	// shells counts the live interactive terminal attaches. A restart would
	// drop each stream under the person typing into it, so they hold the idle
	// check open the way an active run does.
	shells int
}

// credentialUserReservation protects one writable member home from
// ownership changes while its container is live. Root containers do not
// need a reservation because they skip chown.
type credentialUserReservation struct {
	memberID domain.MemberID
	user     string
	owner    string
	run      *supervised
}

// supervised is the in-memory state of one run with a live container.
type supervised struct {
	runID       domain.RunID
	workspaceID domain.WorkspaceID
	containerID runtime.ID
	task        string
	// memberID identifies the persistent home shared by every live run
	// belonging to the member.
	memberID domain.MemberID
	harness  string
	// Mutated only under Scheduler.mu.
	status        domain.RunStatus
	startedAt     time.Time
	paused        bool
	killRequested bool
	killActor     domain.MemberID
	done          chan struct{}
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

type pendingRun struct {
	done          chan struct{}
	killRequested bool
	killActor     domain.MemberID
}

func (s *Scheduler) beginPending(run domain.RunID) *pendingRun {
	pending := &pendingRun{done: make(chan struct{})}
	s.mu.Lock()
	s.pending[run] = pending
	s.mu.Unlock()
	return pending
}

func (s *Scheduler) finishPending(run domain.RunID, pending *pendingRun) {
	s.mu.Lock()
	if s.pending[run] == pending {
		delete(s.pending, run)
		close(pending.done)
	}
	s.mu.Unlock()
}

func (s *Scheduler) waitPending(ctx context.Context, run domain.RunID) error {
	for {
		s.mu.Lock()
		pending := s.pending[run]
		s.mu.Unlock()
		if pending == nil {
			return nil
		}
		select {
		case <-pending.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
	harnesses := defaultHarnesses()
	for name, spec := range cfg.Harnesses {
		if err := validateHarnessSpec(name, spec); err != nil {
			return nil, err
		}
	}
	maps.Copy(harnesses, cfg.Harnesses)
	if cfg.ServerBinary == "" {
		cfg.ServerBinary = DefaultServerBinary
	}
	if cfg.MinFreeBytes == 0 {
		cfg.MinFreeBytes = DefaultMinFreeBytes
	}
	if cfg.Profiles == nil {
		if db, ok := cfg.Store.(*store.DB); ok {
			svc, err := profile.New(db)
			if err != nil {
				return nil, fmt.Errorf("scheduler: profile service: %w", err)
			}
			cfg.Profiles = svc
		}
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return nil, fmt.Errorf("scheduler: create state dir: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		cfg:             cfg,
		harnesses:       harnesses,
		superCtx:        ctx,
		superCancel:     cancel,
		runs:            make(map[domain.RunID]*supervised),
		pending:         make(map[domain.RunID]*pendingRun),
		runShellLocks:   make(map[domain.RunID]*sync.Mutex),
		terminalLocks:   make(map[domain.MemberID]*sync.Mutex),
		terminals:       make(map[domain.MemberID]*terminalSupervision),
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
	if err := s.recoverTerminals(ctx); err != nil {
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
			s.tickUpdates(ctx)
		case <-gcC:
			s.sweepCheckouts(ctx)
		}
	}
}

// Close stops supervision and the Start loops. Containers keep running.
func (s *Scheduler) Close() error {
	s.superCancel()
	s.wg.Wait()
	s.flushPendingRunTitles()
	return nil
}

// ContainerAddr returns the network address of a supervised run container.
func (s *Scheduler) ContainerAddr(ctx context.Context, run domain.RunID) (string, error) {
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil {
		s.mu.Unlock()
		return "", errors.New("run has no live container")
	}
	containerID := entry.containerID
	s.mu.Unlock()
	return s.cfg.Runtime.ContainerIP(ctx, containerID)
}

func validateHarnessSpec(name string, spec HarnessSpec) error {
	registered, known := harness.Lookup(name)
	if spec.Executable == "" {
		// "fake" is a scheduler-owned deterministic harness. Its argv is
		// resolved from AETHER_FAKE_AGENT at launch time, so an explicit
		// empty fixture entry must not be treated as an administrator custom
		// definition.
		if !known && name != "fake" {
			return fmt.Errorf("scheduler: custom harness %q requires an explicit definition", name)
		}
		for _, argv := range [][]string{spec.TUIArgs, spec.HeadlessArgs} {
			if len(argv) > 0 && strings.ContainsAny(argv[0], `/\`) {
				return fmt.Errorf("scheduler: harness %q executable %q is a host path", name, argv[0])
			}
		}
		return nil
	}
	def := harness.Definition{
		Name:            name,
		TUIArgs:         spec.TUIArgs,
		HeadlessArgs:    spec.HeadlessArgs,
		Executable:      spec.Executable,
		ProfileRoot:     spec.ProfileRoot,
		CredentialPaths: spec.CredentialPaths,
		DenyNames:       spec.DenyNames,
	}
	if err := def.Validate(); err != nil {
		return fmt.Errorf("scheduler: harness %q: %w", name, err)
	}
	if known && name != "custom" && registered.Name != name {
		return fmt.Errorf("scheduler: harness %q has invalid registry entry", name)
	}
	return nil
}

// command resolves argv and profile for one launch. Resolution precedence:
// the server-wide admin spec, then the member's own stored definition, then
// the shipped registry. Member definitions only shape argv inside that
// member's own container, so they never leak across members.
func (s *Scheduler) command(ctx context.Context, member domain.MemberID, harnessName string, mode domain.LaunchMode, task string) ([]string, harness.Profile, error) {
	profile, inRegistry := harness.Lookup(harnessName)
	var tui, headless []string
	spec, ok := s.harnesses[harnessName]
	if !ok {
		memberSpec, found, err := s.memberHarnessSpec(ctx, member, harnessName)
		if err != nil {
			return nil, harness.Profile{}, err
		}
		spec, ok = memberSpec, found
	}
	switch {
	case ok:
		tui, headless = spec.TUIArgs, spec.HeadlessArgs
		if spec.Executable != "" {
			profile = (harness.Definition{
				Name:            harnessName,
				TUIArgs:         spec.TUIArgs,
				HeadlessArgs:    spec.HeadlessArgs,
				Executable:      spec.Executable,
				ProfileRoot:     spec.ProfileRoot,
				CredentialPaths: spec.CredentialPaths,
				DenyNames:       spec.DenyNames,
			}).Profile()
		}
		// An explicit argv override is respected verbatim. Registry MCP,
		// session, and resume flags belong to the shipped CLI, not an
		// override: nothing checks the override is still that CLI.
		profile.MCPConfigFlag = ""
		profile.SessionFlag = ""
		profile.SessionResumeFlag = ""
		profile.ResumeFlag = ""
	case inRegistry:
		tui, headless = profile.TUIArgs, profile.HeadlessArgs
	default:
		return nil, harness.Profile{}, fmt.Errorf("scheduler: unknown harness %q; register it with: aether agent add %s", harnessName, harnessName)
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

// pinSession gives a launch a conversation of its own and returns the argv
// to run plus the session ID to record on the run row. Claude Code's
// --session-id names the conversation up front so a later relaunch can name
// it back; a harness without that flag records nothing and relaunches on
// ResumeFlag's best effort.
func pinSession(argv []string, profile harness.Profile) ([]string, string) {
	if profile.SessionFlag == "" {
		return argv, ""
	}
	id := uuid.NewString()
	return harness.WithFlag(argv, profile.SessionFlag, id), id
}

// resumeSession points a relaunch at the interrupted run's own conversation
// and returns the argv plus the session the new row carries forward.
// Resuming by ID names the conversation outright rather than trusting "the
// most recent one in this directory" - every run mounts its checkout at the
// same container path and shares one credential home per member, so that
// guess can land on another run's conversation, even one from another
// workspace.
//
// The pinned ID is only worth naming when this relaunch can reach the
// transcript behind it, which two interrupted rows cannot:
//
//   - The agent never started. recoverUnstarted interrupts queued and
//     provisioning rows too, and the ID is stamped when the row is created,
//     so it names a conversation the harness never opened.
//   - The relaunch is not by the run's owner. Steering others is allowed by
//     default and a handoff transfers the row, but the container mounts the
//     actor's credential home while the transcript lives in the owner's.
//
// claude --resume on an ID it cannot find prints "No conversation found
// with session ID: <id>" and exits 1, which would fail the relaunch
// outright, so both open a fresh conversation instead.
//
// A row with no pinned session at all - a harness that cannot pin, or a row
// written before pinning existed - keeps ResumeFlag's best effort. That
// fallback is sticky: there is no earlier ID left to recover.
func resumeSession(argv []string, profile harness.Profile, old *domain.Run, actor domain.MemberID) ([]string, string) {
	if old.HarnessSessionID == "" || profile.SessionResumeFlag == "" {
		return harness.WithFlag(argv, profile.ResumeFlag, ""), ""
	}
	if old.StartedAt == nil || old.MemberID != actor {
		return pinSession(argv, profile)
	}
	return harness.WithFlag(argv, profile.SessionResumeFlag, old.HarnessSessionID), old.HarnessSessionID
}

// memberHarnessSpec loads and validates the member's stored definition for
// name. A corrupt or invalid stored blob is an error, not a miss: silently
// skipping it would launch a shipped profile the member did not ask for.
// A stored row shadowing a shipped name is rejected here independently of
// the write path, so the invariant holds even against a corrupted store.
func (s *Scheduler) memberHarnessSpec(ctx context.Context, member domain.MemberID, name string) (HarnessSpec, bool, error) {
	if member == "" {
		return HarnessSpec{}, false, nil
	}
	if _, shipped := harness.Lookup(name); shipped || name == "fake" {
		return HarnessSpec{}, false, nil
	}
	row, err := s.cfg.Store.GetHarnessDefinition(ctx, member, name)
	if errors.Is(err, store.ErrNotFound) {
		return HarnessSpec{}, false, nil
	}
	if err != nil {
		return HarnessSpec{}, false, fmt.Errorf("scheduler: load member harness definition: %w", err)
	}
	var def harness.Definition
	if err := json.Unmarshal(row.Definition, &def); err != nil {
		return HarnessSpec{}, false, fmt.Errorf("scheduler: member harness definition %q: %w", name, err)
	}
	if err := harness.ValidateMemberDefinition(def); err != nil {
		return HarnessSpec{}, false, fmt.Errorf("scheduler: member harness definition %q: %w", name, err)
	}
	return HarnessSpec{
		TUIArgs:         def.TUIArgs,
		HeadlessArgs:    def.HeadlessArgs,
		Executable:      def.Executable,
		ProfileRoot:     def.ProfileRoot,
		CredentialPaths: def.CredentialPaths,
		DenyNames:       def.DenyNames,
	}, true, nil
}

// containerSpec converts one fully assembled environment plan into a runtime
// spec. Callers must not assemble workspace mounts or environment fields here.
func (s *Scheduler) containerSpec(run *domain.Run, member *domain.Member, argv []string, plan *EnvironmentPlan) runtime.Spec {
	env := make(map[string]string, len(plan.Env)+7)
	maps.Copy(env, plan.Env)
	env["AETHER_RUN_ID"] = string(run.ID)
	env["AETHER_WORKSPACE_ID"] = string(run.WorkspaceID)
	env["GIT_AUTHOR_NAME"] = member.DisplayName
	env["GIT_COMMITTER_NAME"] = member.DisplayName
	env["GIT_AUTHOR_EMAIL"] = string(member.ID) + "@aether.local"
	env["GIT_COMMITTER_EMAIL"] = string(member.ID) + "@aether.local"
	return runtime.Spec{
		Name:              string(run.ID),
		Image:             plan.Image,
		Env:               env,
		SetupScript:       plan.SetupScript,
		WorktreeHostPath:  run.Worktree,
		WorktreeMountPath: s.cfg.WorktreeMount,
		WorkingDir:        s.cfg.WorktreeMount,
		Command:           argv,
		TTY:               true,
		Mounts:            plan.Mounts,
		User:              plan.User,
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
