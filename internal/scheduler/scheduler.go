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

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/mirror"
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

// BaseCapture is the scheduler's view of immutable workspace-base capture.
// The mirror service implements this seam for both local-only and configured
// workspaces.
type BaseCapture interface {
	Capture(context.Context, domain.WorkspaceID, string) (mirror.CaptureResult, error)
}

// BaseCaptureError preserves the sanitized capture result alongside a
// preflight failure so protocol callers can display a cached retry token
// without creating a run row.
type BaseCaptureError struct {
	Capture mirror.CaptureResult
	Err     error
}

func (e *BaseCaptureError) Error() string {
	if e == nil || e.Err == nil {
		return "scheduler: base capture failed"
	}
	return e.Err.Error()
}

func (e *BaseCaptureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Config wires the scheduler's dependencies and tuning knobs.
type Config struct {
	Store         store.Store
	Runtime       runtime.Runtime
	Bus           events.Bus
	Git           GitEngine
	PTY           PTYHost
	Bases         BaseCapture
	StateDir      string
	Homes         *memberhome.Manager
	Profiles      profileService
	ReposDir      string
	WorktreeMount string
	StandardImage string
	// DefaultStandardImage is the image this build ships with, before any
	// --standard-image the operator set. A server update only moves the
	// standard image when the two are the same.
	DefaultStandardImage string
	StallThreshold       time.Duration
	PollInterval         time.Duration
	StopGrace            time.Duration // default 10s
	CheckoutTTL          time.Duration // default 72h; negative disables GC
	RunContainerTTL      time.Duration // default 1h; negative destroys on close
	// MinFreeBytes is the free-space floor: a launch or relaunch that
	// would start below it is refused with ErrDiskFull rather than filling
	// the disk out from under the runs already on it. Runs already
	// provisioned are never touched - the branch is the artifact and a
	// half-written checkout is worse than a refused one.
	MinFreeBytes int64
	// Harnesses overrides or extends the shipped harness registry
	// (internal/harness: claude, codex, pi, omp, opencode, custom); "fake"
	// (the deterministic e2e agent) is registered here by default. An
	// override replaces the registry argv and keeps the profile's user, key
	// passthrough, and launch environment; it drops the registry's
	// coordination flag, which would be appended to an argv nothing has
	// checked. Member definitions shape argv inside that member's own
	// container and do not leak across members.
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

const DefaultRunContainerTTL = time.Hour

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
	titleMu         sync.Mutex
	titleUpdates    map[domain.RunID]*pendingRunTitle
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
// ownership changes while its container is pending or live. Root containers
// do not need a reservation because they skip chown.
type credentialUserReservation struct {
	memberID domain.MemberID
	user     string
	owner    string
	run      *supervised
	terminal *terminalSupervision
	// pending remains true between reserveTerminalUser and registerTerminal.
	// The reservation must survive registry synchronization during that
	// window, or a concurrent run could chown the shared home first.
	pending bool
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
	// reporter is how much this run's harness can say about its own state
	// (internal/harness). It is fixed at launch, because the reporter is
	// wired into the container's launch command, and recovered from the
	// sidecar rather than recomputed: only the launch knew which profile
	// the container actually got.
	reporter harness.Reporter
	// Mutated only under Scheduler.mu.
	status        domain.RunStatus
	startedAt     time.Time
	paused        bool
	killRequested bool
	killActor     domain.MemberID
	// agentReport is the last thing the agent said about itself, zero until
	// it says anything and again whenever activity un-parks the run. It is
	// only ever set to a report the run's status already matches, so a
	// report the store refused leaves the silence fallback armed. Mirrored
	// into the run's sidecar on every change, so a run the agent parked
	// for its member comes back from a restart still held for them.
	agentReport agentstatus.Report
	// lastWorking is when the agent last said it was working. A report is
	// the only trace its hook leaves - it writes nothing to the terminal
	// and touches no files - so the stall detector counts it as the
	// activity it is, and a run does not park as stalled seconds after the
	// agent proved it is alive.
	lastWorking time.Time
	// parkedAt is when the agent's own waiting report parked this run, and
	// postParkActivity the newest terminal activity seen since. They are
	// what a turn-end reporter is judged on: see unparks. Both zero unless
	// a waiting report is what parked the run.
	parkedAt         time.Time
	postParkActivity time.Time
	launchMode       domain.LaunchMode
	retained         bool
	retainedUntil    *time.Time
	destroyPending   bool
	// finalizing reserves the lifecycle transition after the agent exits.
	// The reservation is brief: finalize's git/runtime/store work runs
	// without lifecycleMu so Kill can still record cancellation.
	finalizing  bool
	done        chan struct{}
	doneOnce    sync.Once
	waitStarted bool
	// lifecycleMu serializes close, relaunch, expiry, and steering admission
	// for this exact container. It is deliberately independent of Scheduler.mu:
	// runtime and git calls must not run while the scheduler lock is held.
	lifecycleMu sync.Mutex
	// runUser is the resolved numeric "uid:gid" the run's container and
	// ownership pass use; empty means root (no ownership pass). Set once
	// the user is resolved during provisioning, or from the sidecar on
	// recovery.
	runUser string
	// home is the container-side HOME resolved when this run was
	// provisioned. Keeping it with supervision avoids reconstructing an old
	// image/profile choice after a restart or handoff.
	home            string
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
	// gitAuthorEmail is the address the container's GIT_AUTHOR_EMAIL was
	// created with. It does not move when the run is handed on or when
	// its owner edits their git identity, so it is what tells the agent's
	// own commits apart from Aether's.
	gitAuthorEmail string
	// coAuthorMu serializes the read-modify-write of this run's co-author
	// list. Two members steering at once would otherwise interleave
	// listing the steerers with writing the file, and the list left on
	// disk would be whichever finished last, not the fuller one.
	coAuthorMu sync.Mutex
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

// startSupervision installs exactly one wait owner for a run. Callers must
// have already installed the entry in s.runs; the helper is safe when two
// recovery paths race to adopt the same sidecar.
func (s *Scheduler) startSupervision(entry *supervised) {
	s.mu.Lock()
	if s.runs[entry.runID] != entry || entry.waitStarted {
		s.mu.Unlock()
		return
	}
	entry.waitStarted = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.superviseWait(entry)
}

func (s *Scheduler) closeDone(entry *supervised) {
	if entry != nil && entry.done != nil {
		entry.doneOnce.Do(func() { close(entry.done) })
	}
}

// RetainsContainer reports whether a durable terminal TUI row still owns a
// container. It intentionally does not consult in-memory state: coordination
// recovery calls it during a fresh process boot. Every terminal sidecar with
// a container ID remains an ownership reference until the scheduler confirms
// destruction and removes it; TTL policy and close reason are deliberately
// not consulted here.
func (s *Scheduler) RetainsContainer(ctx context.Context, run domain.RunID) bool {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil || r.Mode != domain.LaunchTUI || !r.Status.Terminal() {
		return false
	}
	sc, err := s.readSidecar(run)
	if err != nil {
		return false
	}
	mode := sc.Mode
	if mode == "" {
		mode = r.Mode
	}
	if sc.RunID != string(run) || mode != domain.LaunchTUI ||
		(sc.ContainerID == "" && !sc.DestroyPending) {
		return false
	}
	return true
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
	if cfg.RunContainerTTL == 0 {
		cfg.RunContainerTTL = DefaultRunContainerTTL
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

func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.recoverRuns(ctx); err != nil {
		return err
	}
	if err := s.recoverTerminals(ctx); err != nil {
		return err
	}
	interval := s.cfg.PollInterval
	if interval > time.Minute {
		interval = time.Minute
	}
	sweep := time.NewTicker(interval)
	defer sweep.Stop()
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
		case <-sweep.C:
			s.checkStalls(ctx)
			s.sweepRetained(ctx)
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

// UseBaseCapture attaches the immutable base-capture service. The server
// builder uses this after constructing the scheduler because services are
// initialized independently of the core scheduler.
func (s *Scheduler) UseBaseCapture(c BaseCapture) {
	s.mu.Lock()
	s.cfg.Bases = c
	s.mu.Unlock()
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
	task = s.withCoAuthorInstruction(task)
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
		// An explicit argv override is respected verbatim. The registry MCP
		// flag belongs to the shipped CLI, not an override: nothing checks
		// the override is still that CLI.
		profile.MCPConfigFlag = ""
		profile.Reporter = harness.ReporterNone
		profile.StatusArgs = nil
		profile.StatusEnv = nil
		profile.StatusFiles = nil
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

// wrapTUICommand makes the configured harness the first child of a
// POSIX-shell supervisor. Harness arguments remain positional parameters, so
// task text and other argv values can never become shell source. Once the
// harness exits its status is reported and the container stays available via
// a login shell until the scheduler explicitly closes or kills the run.
func wrapTUICommand(argv []string) []string {
	const script = `exec 3<&0
child=
child_signal=TERM
child_signaled=
pending_signal=
pending_status=

forward_shutdown() {
	if [ -n "$child" ] && [ -z "$child_signaled" ]; then
		kill -"$child_signal" "$child" 2>/dev/null || :
		child_signaled=1
	fi
}

request_shutdown() {
	if [ -z "$pending_signal" ]; then
		pending_signal=$1
		pending_status=$2
	fi
	forward_shutdown
}

trap 'request_shutdown TERM 143' TERM
trap 'request_shutdown INT 130' INT
trap 'request_shutdown HUP 129' HUP

run_child() {
	child_signal=$1
	shift
	child_signaled=
	if [ -n "$pending_signal" ]; then
		exit "$pending_status"
	fi
	"$@" <&3 &
	child=$!
	if [ -n "$pending_signal" ]; then
		forward_shutdown
		wait "$child" 2>/dev/null || :
		exit "$pending_status"
	fi
	wait "$child"
	status=$?
	if [ -n "$pending_signal" ]; then
		forward_shutdown
		wait "$child" 2>/dev/null || :
		exit "$pending_status"
	fi
	child=
	child_signaled=
	return "$status"
}

run_child TERM "$@"
status=$?
printf '\n[aether] harness exited with code %s\n' "$status"
while :
do
	if [ -n "$pending_signal" ]; then
		exit "$pending_status"
	fi
	if [ -x /bin/bash ]; then
		run_child HUP /bin/bash -l
	else
		run_child HUP /bin/sh -l
	fi
done`
	command := []string{"/bin/sh", "-c", script, "aether-run-supervisor"}
	return append(command, argv...)
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
	env["AETHER_ACCOUNT_MEMBER_ID"] = string(run.AccountMember())
	identity := member.GitIdentity()
	env["GIT_AUTHOR_NAME"] = identity.Name
	env["GIT_COMMITTER_NAME"] = identity.Name
	env["GIT_AUTHOR_EMAIL"] = identity.Email
	env["GIT_COMMITTER_EMAIL"] = identity.Email
	if run.Mode == domain.LaunchTUI {
		argv = wrapTUICommand(argv)
	}
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
