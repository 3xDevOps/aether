package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/codexapp"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// imageUserResolver is implemented by *runtime.Docker. Runtimes without it
// run images as their default user (root).
type imageUserResolver interface {
	ImageUser(ctx context.Context, ref string) (string, error)
}

// resolveContainerUser resolves root to "" (the image default) so
// Spec.User is only set when it matters.
func (s *Scheduler) resolveContainerUser(ctx context.Context, image string, profile harness.Profile) (string, error) {
	var imageUser string
	if r, ok := s.cfg.Runtime.(imageUserResolver); ok {
		u, err := r.ImageUser(ctx, image)
		if err != nil {
			return "", err
		}
		imageUser = u
	}
	user, err := harness.ResolveUser(profile.User, imageUser)
	if err != nil {
		return "", err
	}
	if user == "0:0" {
		return "", nil
	}
	return user, nil
}

// reserveCredentialUser atomically reserves the uid:gid for a container that
// mounts home and login's login paths. A conflicting live reservation refuses
// it, so no ownership pass can take a home or shared login from a live
// container or flip a login between two recipients.
func (s *Scheduler) reserveCredentialUser(home, login domain.MemberID, user string, sharedHome bool, owner string, run *supervised) (*credentialUserReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncRunUserReservationsLocked()
	if !sharedHome || user == "" {
		if run != nil {
			run.runUser = user
		}
		return nil, nil
	}
	if err := s.reservationConflictLocked(home, login, user, owner); err != nil {
		return nil, err
	}
	reservation := &credentialUserReservation{
		home:  home,
		login: login,
		user:  user,
		owner: owner,
		run:   run,
	}
	s.credentialUsers[reservation] = struct{}{}
	if run != nil {
		run.runUser = user
		run.userReservation = reservation
	}
	return reservation, nil
}

// syncRunUserReservationsLocked folds recovered runs and live terminals into
// the registry. Stale reservations are dropped once their container leaves
// it, except a terminal reservation pending registration.
func (s *Scheduler) syncRunUserReservationsLocked() {
	if s.credentialUsers == nil {
		s.credentialUsers = make(map[*credentialUserReservation]struct{})
	}
	for reservation := range s.credentialUsers {
		if reservation.run != nil {
			if s.runs[reservation.run.runID] == reservation.run && reservation.run.runUser != "" {
				reservation.user = reservation.run.runUser
				continue
			}
			if reservation.run.userReservation == reservation {
				reservation.run.userReservation = nil
			}
			delete(s.credentialUsers, reservation)
			continue
		}
		if reservation.terminal == nil {
			continue
		}
		if reservation.pending {
			continue
		}
		if s.terminals[reservation.terminal.member] == reservation.terminal && reservation.terminal.runUser != "" {
			continue
		}
		if reservation.terminal.userReservation == reservation {
			reservation.terminal.userReservation = nil
		}
		delete(s.credentialUsers, reservation)
	}
	for _, entry := range s.runs {
		if entry.runUser == "" || entry.userReservation != nil {
			continue
		}
		reservation := &credentialUserReservation{
			home:  entry.memberID,
			login: entry.loginMember,
			user:  entry.runUser,
			owner: "live run " + string(entry.runID),
			run:   entry,
		}
		s.credentialUsers[reservation] = struct{}{}
		entry.userReservation = reservation
	}
}

// reservationConflictLocked: the caller must hold s.mu and have synced the
// reservations.
func (s *Scheduler) reservationConflictLocked(home, login domain.MemberID, user, owner string) error {
	for other := range s.credentialUsers {
		if !other.blocks(home, login, user) {
			continue
		}
		if other.home == home {
			return fmt.Errorf("member's environment home %s is reserved by %s as user %s, but %s resolved user %s; concurrent containers for the same member must share one uid:gid mapping",
				home, other.owner, other.user, owner, user)
		}
		return fmt.Errorf("the login %s shares is held by %s as user %s, but %s resolved user %s; a run on a shared account must use the uid:gid of the owner's live containers and of other runs on that login",
			login, other.owner, other.user, owner, user)
	}
	return nil
}

func (s *Scheduler) reserveTerminalUser(entry *terminalSupervision, user string) error {
	if user == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncRunUserReservationsLocked()
	for other := range s.credentialUsers {
		if other.blocks(entry.member, "", user) {
			return fmt.Errorf("member's environment home %s is reserved by %s as user %s, but environment terminal resolved user %s; concurrent containers for the same member must share one uid:gid mapping",
				entry.member, other.owner, other.user, user)
		}
	}
	reservation := &credentialUserReservation{
		home:     entry.member,
		user:     user,
		owner:    "environment terminal " + string(entry.member),
		terminal: entry,
		pending:  true,
	}
	s.credentialUsers[reservation] = struct{}{}
	entry.runUser = user
	entry.userReservation = reservation
	return nil
}

// reserveRunUser records the resolved run user and reserves its writable
// member home and shared login for the full live-run registry lifetime.
func (s *Scheduler) reserveRunUser(entry *supervised, user string, sharedHome bool) error {
	_, err := s.reserveCredentialUser(entry.memberID, entry.loginMember, user, sharedHome, "live run "+string(entry.runID), entry)
	return err
}

// errKillRequested aborts provisioning when a Kill was accepted for the
// in-flight run; failProvisioning turns it into abandoned ("killed").
var errKillRequested = errors.New("scheduler: kill requested during provisioning")

// Launch uses a strict base capture; callers that accept a displayed cached
// base use LaunchWithOptions.
func (s *Scheduler) Launch(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	return s.LaunchWithOptions(ctx, workspace, member, account, task, harness, mode, domain.LaunchOptions{})
}

// LaunchWithOptions captures the base before the run row is created, so a
// failed capture leaves no durable or in-memory run state.
func (s *Scheduler) LaunchWithOptions(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error) {
	for _, id := range append([]domain.WorkspaceID{workspace}, opts.ExtraWorkspaces...) {
		lock := s.workspaceLock(id)
		lock.RLock()
		defer lock.RUnlock()
	}
	if mode == "" {
		mode = domain.LaunchTUI
	}
	extraRepos, err := s.resolveExtras(ctx, opts.ExtraWorkspaces)
	if err != nil {
		return nil, err
	}
	argv, profile, acp, err := s.command(ctx, workspace, member, account, harness, mode, opts.AssignedRunID != "", task, opts.Model, extraRepos...)
	if err != nil {
		return nil, err
	}
	actor, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, err
	}
	if _, accountErr := s.cfg.Store.GetMember(ctx, account); accountErr != nil {
		return nil, accountErr
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	// A reserved identity that already has a run is a replay: its base was
	// captured on the original handoff, and a new fetch could fail or differ.
	reservedID := opts.AssignedRunID
	if reservedID == "" {
		reservedID = opts.ReservedRunID
	}
	if reservedID != "" {
		existing, getErr := s.cfg.Store.GetRun(ctx, reservedID)
		if getErr == nil {
			requested := &domain.Run{
				ID: reservedID, WorkspaceID: workspace,
				MemberID: member, AccountMemberID: account, Task: task,
				Harness: harness, Mode: mode, Model: opts.Model,
			}
			if !sameReservedRun(existing, requested) || (opts.CachedBase != "" && opts.CachedBase != existing.BaseCommit) {
				return nil, errors.New("scheduler: reserved run does not match launch request")
			}
			return s.freshen(ctx, existing), nil
		}
		if !errors.Is(getErr, store.ErrNotFound) {
			return nil, getErr
		}
	}
	release, err := s.reserveCapacity(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	s.mu.Lock()
	bases := s.cfg.Bases
	s.mu.Unlock()
	if bases == nil {
		return nil, errors.New("scheduler: base capture is not configured")
	}
	var extrasCh chan extraCapture
	if len(opts.ExtraWorkspaces) > 0 {
		extrasCh = make(chan extraCapture, 1)
		go func() {
			repos, captureErr := s.captureExtras(ctx, bases, extraRepos)
			extrasCh <- extraCapture{repos, captureErr}
		}()
	}
	var base mirror.CaptureResult
	if opts.FromCommit != "" {
		base = mirror.CaptureResult{Commit: opts.FromCommit, Branch: opts.FromBranch, Source: "run", Configured: true, CheckedAt: time.Now().UTC()}
	} else {
		base, err = bases.Capture(ctx, workspace, opts.CachedBase)
	}
	var extras extraCapture
	if extrasCh != nil {
		extras = <-extrasCh
	}
	if err != nil {
		return nil, &BaseCaptureError{Capture: base, Err: err}
	}
	if extras.err != nil {
		return nil, extras.err
	}
	if opts.CachedBase != "" && (!base.Cached || base.Commit != opts.CachedBase) {
		return nil, &BaseCaptureError{
			Capture: base,
			Err: &gitengine.MirrorError{
				Kind:        gitengine.MirrorErrorInvalidRequest,
				WorkspaceID: workspace,
				Base:        opts.CachedBase,
				Observed:    base.Commit,
			},
		}
	}
	source := base.Source
	switch {
	case base.Cached && source != "":
		source = "cached:" + source
	case base.Cached:
		source = "cached"
	case !base.Configured:
		source = "local"
	}
	run := &domain.Run{
		WorkspaceID:     workspace,
		MemberID:        member,
		AccountMemberID: account,
		Task:            task,
		Harness:         harness,
		Mode:            mode,
		Model:           opts.Model,
		Via:             domain.ViaFrom(ctx),
		ACP:             acp,
		Status:          domain.RunQueued,
		BaseCommit:      base.Commit,
		BaseBranch:      base.Branch,
		BaseSource:      source,
		BaseCheckedAt:   base.CheckedAt,
		Fallback:        opts.Fallback,
		Extras:          extras.repos,
	}
	if opts.RequestedAccount != account {
		run.RequestedAccountMemberID = opts.RequestedAccount
	}
	if err := createAssignedRun(ctx, s.cfg.Store, run, reservedID); err != nil {
		if reservedID != "" {
			existing, getErr := s.cfg.Store.GetRun(ctx, reservedID)
			if getErr == nil && sameReservedRun(existing, run) && (opts.CachedBase == "" || opts.CachedBase == existing.BaseCommit) {
				return s.freshen(ctx, existing), nil
			}
		}
		return nil, err
	}
	for i := range run.Extras {
		run.Extras[i].Key = domain.RepoKey(run.ID, i+1)
	}
	if opts.Created != nil {
		opts.Created(run.ID)
	}
	pending := s.beginPending(run.ID)
	defer s.finishPending(run.ID, pending)
	if opts.Admitted != nil {
		opts.Admitted()
	}
	persistSupervisor := opts.AssignedRunID != ""
	if err := s.provision(ctx, run, ws, actor, argv, profile, persistSupervisor); err != nil {
		return nil, err
	}
	return s.freshen(ctx, run), nil
}

// provision registers the run in s.runs before any provisioning I/O so a
// concurrent Kill takes the supervised path instead of transitioning the row
// underneath the in-flight launch.
func (s *Scheduler) provision(ctx context.Context, run *domain.Run, ws *domain.Workspace, actor *domain.Member, argv []string, profile harness.Profile, persistSupervisor bool) error {
	entry := &supervised{
		runID:           run.ID,
		workspaceID:     run.WorkspaceID,
		task:            run.Task,
		memberID:        run.HomeMember(),
		launchMode:      run.Mode,
		acp:             run.ACP,
		missionAssigned: persistSupervisor,
		status:          domain.RunProvisioning,
		startedAt:       time.Now().UTC(),
		done:            make(chan struct{}),
	}
	s.mu.Lock()
	if pending := s.pending[run.ID]; pending != nil && pending.killRequested {
		entry.killRequested = true
		entry.killActor = pending.killActor
	}
	err := s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunQueued, domain.RunProvisioning, "", actor.ID)
	if err == nil {
		s.runs[run.ID] = entry
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	run.Status = domain.RunProvisioning
	if err := s.provisionSteps(ctx, entry, run, ws, actor, argv, profile, persistSupervisor); err != nil {
		s.failProvisioning(run, actor.ID, err)
		reason := publicRunStatusReason("provisioning: " + err.Error())
		var setup *runtime.SetupError
		if errors.As(err, &setup) {
			return &SetupFailure{run: run.ID, reason: reason, Output: setup.Output}
		}
		return fmt.Errorf("run %s failed: %s", run.ID, reason)
	}
	return nil
}

// Output is for the launching caller alone and is never stored or published.
type SetupFailure struct {
	run    domain.RunID
	reason string
	Output string
}

func (e *SetupFailure) Error() string { return fmt.Sprintf("run %s failed: %s", e.run, e.reason) }

func (s *Scheduler) provisionSteps(ctx context.Context, entry *supervised, run *domain.Run, ws *domain.Workspace, actor *domain.Member, argv []string, profile harness.Profile, persistSupervisor bool) error {
	checkout, branch, err := s.createCheckouts(ctx, run, ws)
	if err != nil {
		return fmt.Errorf("create checkout: %w", err)
	}
	run.Worktree, run.Branch = checkout, branch
	if updateErr := s.cfg.Store.SetRunCheckout(ctx, run.ID, checkout, branch); updateErr != nil {
		return fmt.Errorf("record checkout: %w", updateErr)
	}
	if len(run.Extras) > 0 {
		if updateErr := s.cfg.Store.SetRunRepos(ctx, run.ID, run.Extras); updateErr != nil {
			return fmt.Errorf("record extra repositories: %w", updateErr)
		}
	}
	if pinErr := s.pinLatestProfile(ctx, run); pinErr != nil {
		return fmt.Errorf("pin profile: %w", pinErr)
	}
	unlockCaches := sync.OnceFunc(s.lockRunCaches(run.HomeMember(), run.AccountMember()))
	defer unlockCaches()
	plan, err := s.runPlan(ctx, entry, run, ws, actor, profile)
	if err != nil {
		return err
	}
	s.updateHarness(run, plan, profile)
	// Recorded before coordination is provisioned, because the co-author
	// list written there already leaves this address out: the agent's own
	// commits carry it, so telling the agent to credit it would make it
	// its own co-author.
	s.mu.Lock()
	entry.gitAuthorEmail = actor.GitIdentity().Email
	entry.egress = plan.Egress
	s.mu.Unlock()
	driver, err := s.startRunContainer(ctx, entry, run, actor, argv, profile, plan, persistSupervisor, unlockCaches)
	if err != nil {
		return err
	}
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	if derr := s.cfg.Git.StartDiffWatch(ctx, run.WorkspaceID, run.ID); derr != nil {
		_ = driver.Stop(context.WithoutCancel(ctx), run.ID)
		return s.failContainer(ctx, entry, cid, "start diff watch", derr)
	}
	s.mu.Lock()
	if entry.killRequested {
		err = errKillRequested
	} else {
		err = s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunProvisioning, domain.RunRunning, "", actor.ID)
	}
	s.mu.Unlock()
	if err != nil {
		s.cfg.Git.StopDiffWatch(run.ID)
		_ = driver.Stop(context.WithoutCancel(ctx), run.ID)
		return s.failContainer(ctx, entry, cid, "mark running", err)
	}
	run.Status = domain.RunRunning
	s.startSupervision(entry)
	return nil
}

func (s *Scheduler) runPlan(ctx context.Context, entry *supervised, run *domain.Run, ws *domain.Workspace, actor *domain.Member, profile harness.Profile) (*EnvironmentPlan, error) {
	plan, err := s.BuildEnvironmentPlan(ctx, run, ws, actor, profile, EnvironmentPurposeRun)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	entry.home = plan.Home
	entry.loginMember = plan.LoginMember
	s.mu.Unlock()
	if reserveErr := s.reserveRunUser(entry, plan.User, len(plan.Mounts) > 0); reserveErr != nil {
		return nil, reserveErr
	}
	if ownErr := s.applyRunOwnership(ws, run, entry.memberID, plan.Mounts, plan.User); ownErr != nil {
		return nil, fmt.Errorf("apply run ownership: %w", ownErr)
	}
	if ownErr := s.applyLoginOwnership(entry, plan.LoginMember, plan.Mounts, plan.User); ownErr != nil {
		return nil, fmt.Errorf("apply login ownership: %w", ownErr)
	}
	return plan, nil
}

// startRunContainer is the part of a launch a move to another account
// repeats. On failure no container of it is left running. It calls
// unlockCaches once the sidecar publishes the container's cache ownership.
func (s *Scheduler) startRunContainer(ctx context.Context, entry *supervised, run *domain.Run, actor *domain.Member, argv []string, profile harness.Profile, plan *EnvironmentPlan, persistSupervisor bool, unlockCaches func()) (AgentDriver, error) {
	var native harness.NativeLaunch
	coordination := s.coordinationSeam()
	if coordination != nil && coordination.enabled && run.Mode == domain.LaunchTUI && run.Task != "" {
		var err error
		native, err = profile.PrepareNativeLaunch(coordtransport.MountDir, argv, plan.Env)
		if err != nil {
			return nil, fmt.Errorf("prepare native coordination: %w", err)
		}
	}
	// The app-server wrapper is the staged binary, mounted with coordination.
	appServer := coordination != nil && run.Mode == domain.LaunchTUI && profile.Capabilities.Steer
	if appServer && coordination.enabled {
		native.IdleWake = true
		native.Env = map[string]string{coordtransport.ManagedHooksEnv: "1"}
	}
	// Staging errors fail provisioning: never create a container that lacks the CLI.
	coordMounts, coordArgs, coordEnv, coordErr := s.coordinationMounts(ctx, entry, run, profile, native)
	if coordErr != nil {
		return nil, coordErr
	}
	if run.ACP {
		// The session host reports both ends of every turn itself.
		s.mu.Lock()
		entry.reporter = harness.ReporterFull
		s.mu.Unlock()
	}
	plan.Mounts = append(plan.Mounts, coordMounts...)
	if len(coordMounts) > 0 {
		ensureCoordinationCLIPath(plan.Env)
	}
	if appServer {
		argv = codexapp.LaunchCommand(coordArgs, argv)
	} else {
		argv = append(argv, coordArgs...)
	}
	argv = native.Command(argv)
	// Last, so the server's value wins over the workspace's: what the server
	// needs the container to have is not a preference.
	if err := harness.MergeEnv(plan.Env, coordEnv); err != nil {
		return nil, err
	}
	maps.Copy(plan.Env, native.Env)
	if plan.Egress {
		if s.cfg.Egress == nil {
			return nil, errors.New("workspace egress control needs the Docker runtime")
		}
		proxyEnv, err := s.cfg.Egress.Prepare(ctx)
		if err != nil {
			return nil, fmt.Errorf("prepare the egress proxy: %w", err)
		}
		maps.Copy(plan.Env, proxyEnv)
	}
	s.mu.Lock()
	entry.appServer = appServer
	s.mu.Unlock()
	cid, err := s.cfg.Runtime.Create(ctx, s.containerSpec(run, actor, argv, plan, persistSupervisor))
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}
	s.mu.Lock()
	entry.containerID = cid
	if entry.accountSwap != nil {
		entry.accountSwap.Container = string(cid)
	}
	killed := entry.killRequested
	s.mu.Unlock()
	if killed {
		return nil, s.failContainer(ctx, entry, cid, "create container", errKillRequested)
	}
	if serr := s.cfg.Runtime.Start(ctx, cid); serr != nil {
		return nil, s.failContainer(ctx, entry, cid, "start container", serr)
	}
	s.mu.Lock()
	sc := entry.sidecar()
	s.mu.Unlock()
	if werr := s.writeSidecar(sc); werr != nil {
		return nil, s.failContainer(ctx, entry, cid, "write sidecar", werr)
	}
	unlockCaches()
	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err != nil {
		return nil, s.failContainer(ctx, entry, cid, "attach", err)
	}
	driver := s.entryDriver(entry)
	if perr := driver.Start(ctx, entry, att); perr != nil {
		_ = att.Close()
		return nil, s.failContainer(ctx, entry, cid, "start pty session", perr)
	}
	return driver, nil
}

// failContainer keeps a container whose destroy is uncertain as the run's
// destroy-pending one.
func (s *Scheduler) failContainer(ctx context.Context, entry *supervised, cid runtime.ID, step string, cause error) error {
	if derr := s.cfg.Runtime.Destroy(context.WithoutCancel(ctx), cid); derr != nil &&
		!errors.Is(derr, runtime.ErrNotFound) {
		s.mu.Lock()
		entry.destroyPending = true
		entry.cleanupError = cleanupRuntimeError
		_ = s.writeSidecar(entry.sidecar())
		s.mu.Unlock()
		slog.Warn("scheduler: retain container after failed provisioning destroy", "run", entry.runID, "error", derr)
		return errors.Join(fmt.Errorf("%s: %w", step, cause),
			fmt.Errorf("scheduler: destroy container after failed provisioning: %w", derr))
	}
	return fmt.Errorf("%s: %w", step, cause)
}

// lockRunCaches holds the cache ownership of a container that mounts home
// and account's login across the create/sidecar gap. GC tries this lock
// rather than waiting behind provisioning; never acquire it under
// Scheduler.mu.
func (s *Scheduler) lockRunCaches(home, account domain.MemberID) func() {
	if s.cfg.Homes == nil {
		return func() {}
	}
	members := slices.Compact(slices.Sorted(slices.Values([]domain.MemberID{home, account})))
	unlocks := make([]func(), 0, len(members))
	for _, member := range members {
		if member != "" {
			unlocks = append(unlocks, s.cfg.Homes.LockCaches(member))
		}
	}
	return func() {
		for _, unlock := range slices.Backward(unlocks) {
			unlock()
		}
	}
}

// failProvisioning uses a fresh context so a cancelled launch still lands in
// a consistent state.
func (s *Scheduler) failProvisioning(run *domain.Run, actor domain.MemberID, cause error) {
	slog.Error("scheduler: provisioning failed", "run", run.ID, "error", cause)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.mu.Lock()
	entry := s.runs[run.ID]
	killed := entry != nil && entry.killRequested
	var killActor domain.MemberID
	if killed {
		killActor = entry.killActor
	}
	s.mu.Unlock()
	if killed && run.Worktree != "" {
		if _, cerr := s.commitAll(ctx, run.ID, "wip: "+taskLine(run.Task)); cerr != nil {
			slog.Warn("scheduler: wip commit on kill", "run", run.ID, "error", cerr)
		}
		if _, perr := s.cfg.Git.PublishRunBranch(ctx, run.ID); perr != nil {
			slog.Warn("scheduler: publish branch on kill", "run", run.ID, "error", perr)
		}
	}
	s.mu.Lock()
	var err error
	if killed {
		err = s.transitionOutcomeLocked(ctx, run.ID, run.WorkspaceID, domain.RunProvisioning, domain.RunAbandoned, "killed", killActor, memberCause(killActor))
	} else {
		err = s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunProvisioning, domain.RunFailed,
			"provisioning: "+cause.Error(), actor)
	}
	destroyPending := entry != nil && entry.destroyPending
	if destroyPending && s.runs[run.ID] == entry {
		now := time.Now().UTC()
		entry.retained = true
		entry.retainedUntil = &now
		_ = s.writeSidecar(entry.sidecar())
	}
	s.mu.Unlock()
	if destroyPending {
		return
	}
	if err != nil {
		slog.Warn("scheduler: record provisioning outcome", "run", run.ID, "error", err)
	}
	s.removeSidecar(run.ID)
	s.closeDone(entry)
	s.mu.Lock()
	if s.runs[run.ID] == entry {
		delete(s.runs, run.ID)
	}
	s.mu.Unlock()

}

// freshen re-reads the run so callers see the row exactly as persisted
// (StartedAt, and any transition that raced the return).
func (s *Scheduler) freshen(ctx context.Context, run *domain.Run) *domain.Run {
	if fresh, err := s.cfg.Store.GetRun(ctx, run.ID); err == nil {
		return fresh
	}
	return run
}

// command resolves argv and profile for one launch by member in workspace on
// account's shared account, with the profile from launchProfile. An acp
// launch has no argv: the task travels over the protocol.
func (s *Scheduler) command(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, harnessName string, mode domain.LaunchMode, assigned bool, task, model string, extras ...domain.RunRepo) (argv []string, profile harness.Profile, acp bool, err error) {
	if err = domain.ValidModel(model); err != nil {
		return nil, harness.Profile{}, false, fmt.Errorf("scheduler: %w", err)
	}
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
	if model != "" && len(profile.ModelArgs) == 0 {
		return nil, harness.Profile{}, false, fmt.Errorf("scheduler: agent %q has no model flag; launch it without a model", harnessName)
	}
	if task != "" && !profile.CarriesInstructions() {
		instructions, ierr := s.briefedInstructions(ctx, s.runGuide(ctx, "", assigned, mode == domain.LaunchHeadless, false), extras, workspace, member)
		if ierr != nil {
			return nil, harness.Profile{}, false, fmt.Errorf("scheduler: %w", ierr)
		}
		task = withInstructions(task, instructions)
	}
	return harness.Argv(argv, task, profile.ModelLaunchArgs(model)), profile, false, nil
}
