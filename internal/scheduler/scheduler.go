// Package scheduler owns the run lifecycle: provisioning, legal status
// transitions, supervision, stall detection, recovery and checkout GC. It is the
// single writer of run statuses; docs/failure-handling.md covers its guards.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

var ErrInvalidTransition = errors.New("scheduler: invalid run state transition")

// ErrDiskFull refuses a new run below Config.MinFreeBytes; nothing is created
// and existing runs keep going.
var ErrDiskFull = errors.New("scheduler: not enough free disk space to start a new run")

// DefaultMinFreeBytes leaves headroom for a new run's checkout, container
// writes and event log.
const DefaultMinFreeBytes = 1 << 30

type BaseCapture interface {
	Capture(context.Context, domain.WorkspaceID, string) (mirror.CaptureResult, error)
}

// BaseCaptureError keeps the sanitized capture result so callers can show a
// cached retry token without creating a run row.
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

type Config struct {
	Store                       store.Store
	Runtime                     runtime.Runtime
	Bus                         events.Bus
	Git                         GitEngine
	PTY                         PTYHost
	Bases                       BaseCapture
	StateDir                    string
	Homes                       *memberhome.Manager
	Profiles                    profileService
	ReposDir                    string
	WorktreeMount               string
	StandardImage               string
	BrowserImage                string
	Control                     *control.Service
	DevelopmentTerminalTakeover func(context.Context, domain.RunID, domain.MemberID) error
	// DefaultStandardImage is this build's image; a server update moves the
	// standard image only when StandardImage still equals it.
	DefaultStandardImage string
	StallThreshold       time.Duration
	PollInterval         time.Duration
	StopGrace            time.Duration // default 10s
	CheckoutTTL          time.Duration // default 72h; negative disables GC
	RunContainerTTL      time.Duration // default 168h; negative destroys on close/completion
	// ExitProbeTimeout bounds recovery's startup probe of whether a container
	// exited before attach.
	ExitProbeTimeout time.Duration
	// Now defaults to time.Now; tests set it to control the second-granularity
	// saved-environment image tag.
	Now func() time.Time
	// MinFreeBytes refuses a launch or relaunch below it with ErrDiskFull. Runs
	// already provisioned are never touched: a half-written checkout is worse
	// than a refused one.
	MinFreeBytes int64
	// Harnesses overrides or extends the shipped harness registry. An override
	// replaces the registry argv and drops its coordination flag, which would be
	// appended to an argv nothing has checked.
	Harnesses map[string]HarnessSpec
	// ServerBinary is staged into run and terminal containers for the
	// coordination CLI; empty means DefaultServerBinary.
	ServerBinary string
	// HarnessUpdateDisabled stops pre-launch updates of shipped harnesses
	// installed in the member home.
	HarnessUpdateDisabled bool
	// Test-only overrides of the matching default* constants.
	turnTail             time.Duration
	harnessUpdateTimeout time.Duration
	harnessUpdateWait    time.Duration
}

const DefaultRunContainerTTL = 7 * 24 * time.Hour

// DefaultServerBinary is the running server binary, /proc/self/exe rather
// than os.Args[0].
const DefaultServerBinary = "/proc/self/exe"

// HarnessSpec is an administrator-supplied harness definition. An empty
// Executable keeps the argv-only override for shipped profiles and "fake".
type HarnessSpec struct {
	TUIArgs         []string
	HeadlessArgs    []string
	ACPArgs         []string
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
		"fake": {},
	}
}

// Scheduler is the run lifecycle engine behind the sshd.RunController seam.
type Scheduler struct {
	cfg       Config
	harnesses map[string]HarnessSpec

	// recoveryReady closes after startup reconciliation; room delivery waits
	// for it so overdue steers cannot claim before recovered PTYs are injectable.
	recoveryReady     chan struct{}
	recoveryReadyOnce sync.Once
	// superCtx bounds every supervision goroutine; cancelling it never stops a
	// container.
	superCtx    context.Context
	superCancel context.CancelFunc
	wg          sync.WaitGroup

	mu   sync.Mutex
	runs map[domain.RunID]*supervised
	// archiveMu serializes SetArchived against Relaunch restoring an archived run.
	archiveMu sync.Mutex
	// workspaceLocks fence launch and retained relaunch during deletion.
	workspaceLocks map[domain.WorkspaceID]*sync.RWMutex
	// homeLocks serialize the ownership passes that chown inside one
	// member's home, so a long walk there never holds mu.
	homeLocks map[domain.MemberID]*sync.Mutex
	// pending holds runs whose row exists but whose provisioning has not reached
	// runs yet; Delete waits on it and Kill records its request for the handoff.
	pending map[domain.RunID]*pendingRun
	// runShellLocks serialize shell lifecycle within one run.
	runShellLocks map[domain.RunID]*sync.Mutex
	// runShellReservationMu protects the cross-run reservation map. A Go map
	// cannot be written under independent per-run locks.
	runShellReservationMu sync.Mutex
	runShellReservations  map[string]*shellTabState
	runShellTerminals     map[domain.RunID]*runTerminalSet
	developmentMu         sync.Mutex
	development           *developmentState
	terminalLocks         map[domain.MemberID]*sync.Mutex
	terminals             map[domain.MemberID]*terminalSupervision
	credentialUsers       map[*credentialUserReservation]struct{}
	titleMu               sync.Mutex
	titleUpdates          map[domain.RunID]*pendingRunTitle
	// coordination is nil when new containers get no coordination assets.
	coordination *coordination
	// evidence is nil when evidence capture is not enabled.
	evidence EvidenceService
	// updates is nil when a scheduled update never applies.
	updates UpdateTicker
	// shells counts live terminal attaches, which hold the idle-restart check
	// open like an active run, since a restart would drop them.
	shells         int
	harnessUpdates map[harnessUpdateKey]*harnessUpdateState
	// agentInstalls maps a member with an agent.install in flight to its home's
	// host path.
	agentInstalls map[domain.MemberID]string
	acp           *acpDriver
}

// credentialUserReservation stops ownership changes to the member home and
// shared login a pending or live container mounts. Root containers skip chown
// and need none.
type credentialUserReservation struct {
	home domain.MemberID
	// login is the account owner whose login paths the container mounts,
	// empty when it mounts none.
	login    domain.MemberID
	user     string
	owner    string
	run      *supervised
	terminal *terminalSupervision
	// pending covers reserveTerminalUser to registerTerminal; the reservation
	// must survive registry sync then, or a concurrent run could chown the home.
	pending bool
}

// blocks reports whether r keeps a container that mounts home, and login's
// login paths when login is set, from running as user. The owner of a home
// always wins: a reservation that only mounts a login from home never blocks
// a container whose home it is, while the owner's live containers and other
// runs on the same login block a run on that login with another mapping.
func (r *credentialUserReservation) blocks(home, login domain.MemberID, user string) bool {
	if r.user == user {
		return false
	}
	if r.home == home {
		return true
	}
	return login != "" && (r.home == login || r.login == login)
}

// supervised is the in-memory state of one run with a live container.
type supervised struct {
	runID       domain.RunID
	workspaceID domain.WorkspaceID
	containerID runtime.ID
	task        string
	// memberID identifies the persistent home the container mounts
	// (domain.Run.HomeMember), shared by every live container of that member.
	memberID domain.MemberID
	// loginMember is the account owner whose login paths the container
	// mounts; empty when none.
	loginMember domain.MemberID
	// reporter is recovered from the sidecar, since only the launch or mode
	// switch knew which command the container runs.
	reporter harness.Reporter
	// Mutated only under Scheduler.mu.
	status        domain.RunStatus
	startedAt     time.Time
	paused        bool
	killRequested bool
	killActor     domain.MemberID
	// agentReport is the last execution report only; input deltas are never
	// retained or replayed. A turn-end idle report that starts a reported finish
	// is recorded without the park (see ReportAgentState).
	agentReport agentstatus.Report
	// pendingInputs is an immutable, sorted set for this execution lifetime.
	pendingInputs []domain.RunInputRequest
	// inputPublishPending keeps the current snapshot owed to the event log,
	// including an empty set after the last request closes.
	inputPublishPending bool
	// lastWorking is when the agent last said it was working. The hook leaves
	// no other trace, so the stall detector counts it as activity.
	lastWorking time.Time
	// parkedAt and postParkActivity judge a turn-end reporter (see unparks);
	// both are zero unless the agent's own waiting report parked the run.
	parkedAt         time.Time
	postParkActivity time.Time
	launchMode       domain.LaunchMode
	// acp mirrors Run.ACP.
	acp             bool
	missionAssigned bool
	retained        bool
	retainedUntil   *time.Time
	destroyPending  bool
	// evidencePending persists a completed terminalization whose required
	// evidence capture did not finish, so a same-status retry resolves it first.
	evidencePending bool
	// finalizing reserves the post-exit transition briefly; finalize's work
	// runs without lifecycleMu so Kill can still record cancellation.
	finalizing  bool
	done        chan struct{}
	doneOnce    sync.Once
	waitStarted bool
	// lifecycleMu serializes close, relaunch, expiry and steering admission for
	// this container. It is separate from Scheduler.mu: runtime and git calls
	// must not run under the scheduler lock.
	lifecycleMu sync.Mutex
	// runUser is the resolved "uid:gid" for the container and ownership pass;
	// empty means root (no ownership pass).
	runUser string
	// home is the container-side HOME resolved at provisioning, kept so a
	// restart need not reconstruct an old image/profile choice.
	home            string
	userReservation *credentialUserReservation
	// exitObserved / exitCode are the durable Wait result, persisted
	// before finalize so a crash can resume the original exit.
	exitObserved bool
	exitCode     int
	// evidenceIdentity is the stable finish-capture identity. It is mirrored
	// to the sidecar so a retry after a crash cannot create another packet.
	evidenceIdentity string
	// The coordination assets mounted into this run's container, mirrored
	// into the sidecar before the container is created (coordination.go).
	bridgeDigest string
	bridgePath   string
	coordDir     string
	// gitAuthorEmail is fixed at container creation, so it tells the agent's
	// own commits apart from Aether's even after a handoff or identity edit.
	gitAuthorEmail string
	agentSessionID string
	sessionMu      sync.Mutex
	agentExec      *runtime.ExecIdentity
	// switching is the mode a mode switch is moving the run to, empty when
	// none is in flight. The switch holds lifecycleMu throughout.
	switching    domain.LaunchMode
	switchIntent *switchIntent
	// coAuthorMu serializes the co-author list's read-modify-write so
	// concurrent steers cannot leave the shorter list on disk.
	coAuthorMu sync.Mutex
	// reported is the outcome a terminal coord.report armed (empty when
	// unarmed), reportedAt when it was armed, and reportFinishing is set while
	// finishReported owns the finish.
	reported        domain.RunStatus
	reportedAt      time.Time
	reportFinishing bool
	// blockedReason is the latest blocked report as a status reason and
	// blockedShown whether a park has shown it. blockedReportID/At outlive the
	// reason so a replay or an older retried report cannot bring it back.
	blockedReason   string
	blockedShown    bool
	blockedReportID string
	blockedReportAt time.Time
	// relaunchedAt is when the last relaunch reopened the run; a report
	// finalized before it belongs to the launch that relaunch ended.
	relaunchedAt time.Time
}

type pendingRun struct {
	done          chan struct{}
	killRequested bool
	killActor     domain.MemberID
}

func (s *Scheduler) beginPending(run domain.RunID) *pendingRun {
	pending := &pendingRun{done: make(chan struct{})}
	s.mu.Lock()
	if s.pending == nil {
		s.pending = make(map[domain.RunID]*pendingRun)
	}
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

// startSupervision installs exactly one wait owner for a run already in
// s.runs; two recovery paths may race to adopt the same sidecar.
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

// RetainsContainer reports whether a durable terminal row still owns a
// container. It ignores in-memory state, TTL and close reason: coordination
// recovery calls it on a fresh boot, and a sidecar with a container ID stays
// an ownership reference until destruction is confirmed.
func (s *Scheduler) RetainsContainer(ctx context.Context, run domain.RunID) bool {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil || !r.Status.Terminal() {
		return false
	}
	sc, err := s.readSidecar(run)
	if err != nil {
		return false
	}
	if sc.RunID != string(run) ||
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
	if cfg.ExitProbeTimeout <= 0 {
		cfg.ExitProbeTimeout = defaultExitProbeTimeout
	}
	if cfg.turnTail <= 0 {
		cfg.turnTail = defaultTurnTail
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
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
	s := &Scheduler{
		cfg:                  cfg,
		harnesses:            harnesses,
		superCtx:             ctx,
		superCancel:          cancel,
		recoveryReady:        make(chan struct{}),
		runs:                 make(map[domain.RunID]*supervised),
		runShellLocks:        make(map[domain.RunID]*sync.Mutex),
		runShellReservations: make(map[string]*shellTabState),
		terminalLocks:        make(map[domain.MemberID]*sync.Mutex),
		terminals:            make(map[domain.MemberID]*terminalSupervision),
		credentialUsers:      make(map[*credentialUserReservation]struct{}),
	}
	s.acp = newACPDriver(s)
	return s, nil
}

// RecoveryReady returns a channel closed once all persisted runtime state has
// been reconciled and recovered sessions are eligible for injection.
func (s *Scheduler) RecoveryReady() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.recoveryReady
}

func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.recoverRuns(ctx); err != nil {
		return recoveryError(err)
	}
	if err := s.recoverTerminals(ctx); err != nil {
		return recoveryError(err)
	}
	if err := s.recoverDevelopment(ctx); err != nil {
		return recoveryError(err)
	}
	s.recoveryReadyOnce.Do(func() { close(s.recoveryReady) })
	interval := s.cfg.PollInterval
	if interval > time.Minute {
		interval = time.Minute
	}
	sweep := time.NewTicker(interval)
	defer sweep.Stop()
	// sweepArchived runs regardless of CheckoutTTL: archive retention is
	// unconditional, unlike checkout GC.
	if s.cfg.CheckoutTTL > 0 {
		s.sweepCheckouts(ctx)
	}
	s.sweepArchived(ctx)
	gc := time.NewTicker(time.Hour)
	defer gc.Stop()
	for {
		select {
		case <-ctx.Done():
			s.superCancel()
			return nil
		case <-s.superCtx.Done():
			return nil
		case <-sweep.C:
			s.checkStalls(ctx)
			s.finishOverdueReports()
			s.sweepRetained(ctx)
			s.drainEvidencePublications(ctx)
			s.tickUpdates(ctx)
		case <-gc.C:
			if s.cfg.CheckoutTTL > 0 {
				s.sweepCheckouts(ctx)
			}
			s.sweepArchived(ctx)
		}
	}
}

// recoveryError drops only the cancellation the caller's own shutdown caused,
// so a stopped scheduler reports a clean shutdown while a real failure that
// merely raced it still reaches Server.Run. Matching the error is exact:
// modernc.org/sqlite and database/sql both surface an interrupted statement or
// Rows as ctx.Err(), so it wraps context.Canceled.
func recoveryError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

// Close stops supervision and the Start loops. Containers keep running.
func (s *Scheduler) Close() error {
	s.superCancel()
	s.wg.Wait()
	s.acp.shutdown()
	s.flushPendingRunTitles()
	return s.DetachDevelopmentTerminals(context.Background())
}

// UseBaseCapture attaches the base-capture service, built after the scheduler.
func (s *Scheduler) UseBaseCapture(c BaseCapture) {
	s.mu.Lock()
	s.cfg.Bases = c
	s.mu.Unlock()
}

// UseEvidence attaches the evidence service, built after the scheduler.
func (s *Scheduler) UseEvidence(e EvidenceService) {
	s.mu.Lock()
	s.evidence = e
	s.mu.Unlock()
}

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
		// "fake" resolves its argv from AETHER_FAKE_AGENT at launch, so an
		// empty fixture entry is not a custom definition.
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
		ACPArgs:         spec.ACPArgs,
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

// command resolves argv and profile for one launch by member on account's
// shared account, with the profile from launchProfile. An acp launch has no
// argv: the task travels over the protocol.
func (s *Scheduler) command(ctx context.Context, member, account domain.MemberID, harnessName string, mode domain.LaunchMode, task string) (argv []string, profile harness.Profile, acp bool, err error) {
	task = s.withCoAuthorInstruction(task)
	profile, argvs, err := s.launchProfile(ctx, member, account, harnessName)
	if err != nil {
		return nil, harness.Profile{}, false, err
	}
	if !mode.Valid() {
		return nil, harness.Profile{}, false, fmt.Errorf("scheduler: invalid launch mode %q", mode)
	}
	acp = mode == domain.LaunchACP ||
		(mode == domain.LaunchHeadless && s.backgroundACP(member, account, profile, argvs))
	argv = argvs[mode]
	if acp {
		argv = argvs[domain.LaunchACP]
	}
	if harnessName == "fake" && len(argv) == 0 && !acp {
		argv = strings.Fields(os.Getenv(fakeAgentEnv))
	}
	if len(argv) == 0 {
		return nil, harness.Profile{}, false, fmt.Errorf("scheduler: harness %q has no command for mode %q", harnessName, mode)
	}
	if acp {
		return nil, profile, true, nil
	}
	return harness.Argv(argv, task), profile, false, nil
}

func (s *Scheduler) backgroundACP(member, account domain.MemberID, profile harness.Profile, argvs map[domain.LaunchMode][]string) bool {
	adapter := argvs[domain.LaunchACP]
	if !profile.ACPDefault || len(adapter) == 0 || s.cfg.Homes == nil {
		return false
	}
	cli := adapter[0]
	if tui := argvs[domain.LaunchTUI]; len(tui) > 0 {
		cli = tui[0]
	}
	_, installed, err := s.cfg.Homes.AgentInstalled(member, account, cli, adapter[0], profile.InstallPaths)
	if err != nil {
		slog.Warn("scheduler: look for the agent's ACP server; the background run uses its command line", "agent", profile.Name, "error", err)
	}
	return installed
}

// errMemberDefinitionOnly refuses member's own harness definition on
// another member's account. A member definition declares no login the
// owner agreed to share, so only server-wide definitions can.
var errMemberDefinitionOnly = errors.New("is your own agent definition, which runs only on your own account; on a shared account, only a server-wide definition (aether-server --harness-definitions) can declare the login it shares")

// launchProfile resolves the profile and argv templates. Precedence: the
// server-wide admin spec, then member's stored definition, then the shipped
// registry. Only server-controlled CredentialPaths decide what a share
// exposes, so member's own definition is refused on another member's account.
func (s *Scheduler) launchProfile(ctx context.Context, member, account domain.MemberID, harnessName string) (harness.Profile, map[domain.LaunchMode][]string, error) {
	profile, inRegistry := harness.Lookup(harnessName)
	var tui, headless, acp []string
	spec, ok := s.harnesses[harnessName]
	memberDefined := false
	if !ok {
		memberSpec, found, err := s.memberHarnessSpec(ctx, member, harnessName)
		if err != nil {
			return harness.Profile{}, nil, err
		}
		spec, ok, memberDefined = memberSpec, found, found
	}
	switch {
	case ok:
		tui, headless, acp = spec.TUIArgs, spec.HeadlessArgs, spec.ACPArgs
		if spec.Executable != "" {
			profile = (harness.Definition{
				Name:            harnessName,
				TUIArgs:         spec.TUIArgs,
				HeadlessArgs:    spec.HeadlessArgs,
				ACPArgs:         spec.ACPArgs,
				Executable:      spec.Executable,
				ProfileRoot:     spec.ProfileRoot,
				CredentialPaths: spec.CredentialPaths,
				DenyNames:       spec.DenyNames,
			}).Profile()
		}
		// An explicit argv override is respected verbatim. Registry discovery
		// and status assets belong to the shipped CLI, not an override.
		profile.Reporter = harness.ReporterNone
		profile.StatusArgs = nil
		profile.StatusEnv = nil
		profile.StatusFiles = nil
		profile.DiscoveryArgs = nil
		profile.DiscoveryEnv = nil
		profile.DiscoveryFiles = nil
		profile.NativeCoordination = false
		profile.UpdateScript = ""
		profile.SwitchVerified = false
	case inRegistry:
		tui, headless, acp = profile.TUIArgs, profile.HeadlessArgs, profile.ACPArgs
	default:
		return harness.Profile{}, nil, fmt.Errorf("scheduler: unknown harness %q; register it with: aether agent add %s", harnessName, harnessName)
	}
	if memberDefined && account != member {
		return harness.Profile{}, nil, fmt.Errorf("scheduler: harness %q %w", harnessName, errMemberDefinitionOnly)
	}
	return profile, map[domain.LaunchMode][]string{domain.LaunchTUI: tui, domain.LaunchHeadless: headless, domain.LaunchACP: acp}, nil
}

// memberHarnessSpec loads the member's stored definition for name. A corrupt
// blob is an error, not a miss, or a shipped profile the member did not ask
// for would launch. A row shadowing a shipped name is rejected here too, in
// case the store is corrupted.
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
		ACPArgs:         def.ACPArgs,
		Executable:      def.Executable,
		ProfileRoot:     def.ProfileRoot,
		CredentialPaths: def.CredentialPaths,
		DenyNames:       def.DenyNames,
	}, true, nil
}

// containerSpec converts one fully assembled environment plan into a runtime
// spec. Callers must not assemble workspace mounts or environment fields here.
func (s *Scheduler) containerSpec(run *domain.Run, member *domain.Member, argv []string, plan *EnvironmentPlan, persistSupervisor bool) runtime.Spec {
	env := make(map[string]string, len(plan.Env)+8)
	maps.Copy(env, plan.Env)
	env["AETHER_RUN_ID"] = string(run.ID)
	env["AETHER_WORKSPACE_ID"] = string(run.WorkspaceID)
	env["AETHER_ACCOUNT_MEMBER_ID"] = string(run.AccountMember())
	env["AETHER_HARNESS"] = run.Harness
	identity := member.GitIdentity()
	env["GIT_AUTHOR_NAME"] = identity.Name
	env["GIT_COMMITTER_NAME"] = identity.Name
	env["GIT_AUTHOR_EMAIL"] = identity.Email
	env["GIT_COMMITTER_EMAIL"] = identity.Email
	if run.ACP {
		// An adapter cannot open a browser in a container; its login
		// prints a URL instead.
		env["NO_BROWSER"] = "1"
	}
	if run.Mode == domain.LaunchACP {
		env[coordtransport.EnhancedEnv] = "1"
	}
	if run.Mode.Interactive() || persistSupervisor || run.ACP {
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
