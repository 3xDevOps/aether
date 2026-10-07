package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/harness"
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

// checkFreeSpace runs before the run row exists so a refusal leaves nothing
// behind. An unreadable filesystem is not treated as full: the floor exists
// to stop a disk filling, not to stop the server.
func (s *Scheduler) checkFreeSpace() error {
	if s.cfg.MinFreeBytes < 0 {
		return nil
	}
	free, err := disk.Free(s.cfg.StateDir)
	if err != nil {
		slog.Warn("scheduler: free-space floor: reading the filesystem failed; allowing the run",
			"dir", s.cfg.StateDir, "error", err)
		return nil
	}
	if free >= uint64(s.cfg.MinFreeBytes) {
		return nil
	}
	return fmt.Errorf("%w: %d bytes free, floor is %d bytes; finished-run checkouts are "+
		"garbage-collected after their TTL, and the dashboard's disk gauge shows what is holding the space",
		ErrDiskFull, free, s.cfg.MinFreeBytes)
}

// Launch uses a strict base capture; callers that accept a displayed cached
// base use LaunchWithOptions.
func (s *Scheduler) Launch(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	return s.LaunchWithOptions(ctx, workspace, member, account, task, harness, mode, domain.LaunchOptions{})
}

// LaunchWithOptions captures the base before the run row is created, so a
// failed capture leaves no durable or in-memory run state.
func (s *Scheduler) LaunchWithOptions(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error) {
	lock := s.workspaceLock(workspace)
	lock.RLock()
	defer lock.RUnlock()
	if mode == "" {
		mode = domain.LaunchTUI
	}
	if err := s.checkFreeSpace(); err != nil {
		return nil, err
	}
	argv, profile, acp, err := s.command(ctx, member, account, harness, mode, task)
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
	if opts.AssignedRunID != "" {
		existing, getErr := s.cfg.Store.GetRun(ctx, opts.AssignedRunID)
		if getErr == nil {
			requested := &domain.Run{
				ID: opts.AssignedRunID, WorkspaceID: workspace,
				MemberID: member, AccountMemberID: account, Task: task,
				Harness: harness, Mode: mode,
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
	s.mu.Lock()
	bases := s.cfg.Bases
	s.mu.Unlock()
	if bases == nil {
		return nil, errors.New("scheduler: base capture is not configured")
	}
	base, err := bases.Capture(ctx, workspace, opts.CachedBase)
	if err != nil {
		return nil, &BaseCaptureError{Capture: base, Err: err}
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
		ACP:             acp,
		Status:          domain.RunQueued,
		BaseCommit:      base.Commit,
		BaseBranch:      base.Branch,
		BaseSource:      source,
		BaseCheckedAt:   base.CheckedAt,
	}
	if err := createAssignedRun(ctx, s.cfg.Store, run, opts.AssignedRunID); err != nil {
		if opts.AssignedRunID != "" {
			existing, getErr := s.cfg.Store.GetRun(ctx, opts.AssignedRunID)
			if getErr == nil && sameReservedRun(existing, run) && (opts.CachedBase == "" || opts.CachedBase == existing.BaseCommit) {
				return s.freshen(ctx, existing), nil
			}
		}
		return nil, err
	}
	pending := s.beginPending(run.ID)
	defer s.finishPending(run.ID, pending)
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
		return errors.New(publicRunStatusReason("provisioning: " + err.Error()))
	}
	return nil
}

func (s *Scheduler) provisionSteps(ctx context.Context, entry *supervised, run *domain.Run, ws *domain.Workspace, actor *domain.Member, argv []string, profile harness.Profile, persistSupervisor bool) error {
	checkout, branch, err := s.cfg.Git.CreateRunCheckoutAt(ctx, ws.ID, run.ID, run.BaseCommit, run.BaseBranch, run.Task, ws.Origin)
	if err != nil {
		return fmt.Errorf("create checkout: %w", err)
	}
	run.Worktree, run.Branch = checkout, branch
	if updateErr := s.cfg.Store.UpdateRun(ctx, run); updateErr != nil {
		return fmt.Errorf("record checkout: %w", updateErr)
	}
	if pinErr := s.pinLatestProfile(ctx, run); pinErr != nil {
		return fmt.Errorf("pin profile: %w", pinErr)
	}
	plan, err := s.BuildEnvironmentPlan(ctx, run, ws, actor, profile, EnvironmentPurposeRun)
	if err != nil {
		return err
	}
	s.mu.Lock()
	entry.home = plan.Home
	entry.loginMember = plan.LoginMember
	s.mu.Unlock()
	if reserveErr := s.reserveRunUser(entry, plan.User, len(plan.Mounts) > 0); reserveErr != nil {
		return reserveErr
	}
	if ownErr := s.applyRunOwnership(ws, run, entry.memberID, plan.Mounts, plan.User); ownErr != nil {
		return fmt.Errorf("apply run ownership: %w", ownErr)
	}
	if ownErr := s.applyLoginOwnership(entry, plan.LoginMember, plan.Mounts, plan.User); ownErr != nil {
		return fmt.Errorf("apply login ownership: %w", ownErr)
	}
	s.updateHarness(ctx, run, plan, profile)
	var native harness.NativeLaunch
	if coordination := s.coordinationSeam(); coordination != nil && coordination.enabled &&
		run.Mode == domain.LaunchTUI && run.Task != "" {
		native, err = profile.PrepareNativeLaunch(coordtransport.MountDir, argv, plan.Env)
		if err != nil {
			return fmt.Errorf("prepare native coordination: %w", err)
		}
	}
	// Recorded before coordination is provisioned, because the co-author
	// list written there already leaves this address out: the agent's own
	// commits carry it, so telling the agent to credit it would make it
	// its own co-author.
	s.mu.Lock()
	entry.gitAuthorEmail = actor.GitIdentity().Email
	s.mu.Unlock()
	// Staging errors fail provisioning: never create a container that lacks the CLI.
	coordMounts, coordArgs, coordEnv, coordErr := s.coordinationMounts(ctx, entry, run, profile, native)
	if coordErr != nil {
		return coordErr
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
	argv = append(argv, coordArgs...)
	argv = native.Command(argv)
	// Last, so the server's value wins over the workspace's: what the server
	// needs the container to have is not a preference.
	if err = harness.MergeEnv(plan.Env, coordEnv); err != nil {
		return err
	}
	maps.Copy(plan.Env, native.Env)
	cid, err := s.cfg.Runtime.Create(ctx, s.containerSpec(run, actor, argv, plan, persistSupervisor))
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	fail := func(step string, cause error) error {
		if derr := s.cfg.Runtime.Destroy(context.WithoutCancel(ctx), cid); derr != nil &&
			!errors.Is(derr, runtime.ErrNotFound) {
			s.mu.Lock()
			entry.destroyPending = true
			_ = s.writeSidecar(entry.sidecar())
			s.mu.Unlock()
			slog.Warn("scheduler: retain container after failed provisioning destroy", "run", run.ID, "error", derr)
			return errors.Join(fmt.Errorf("%s: %w", step, cause),
				fmt.Errorf("scheduler: destroy container after failed provisioning: %w", derr))
		}
		s.removeSidecar(run.ID)
		return fmt.Errorf("%s: %w", step, cause)
	}
	s.mu.Lock()
	entry.containerID = cid
	killed := entry.killRequested
	s.mu.Unlock()
	if killed {
		return fail("create container", errKillRequested)
	}
	if serr := s.cfg.Runtime.Start(ctx, cid); serr != nil {
		return fail("start container", serr)
	}
	s.mu.Lock()
	sc := entry.sidecar()
	s.mu.Unlock()
	if werr := s.writeSidecar(sc); werr != nil {
		return fail("write sidecar", werr)
	}
	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err != nil {
		return fail("attach", err)
	}
	driver := s.driver(run.ACP)
	if perr := driver.Start(ctx, entry, att); perr != nil {
		_ = att.Close()
		return fail("start pty session", perr)
	}
	if derr := s.cfg.Git.StartDiffWatch(ctx, run.WorkspaceID, run.ID); derr != nil {
		_ = driver.Stop(context.WithoutCancel(ctx), run.ID)
		return fail("start diff watch", derr)
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
		return fail("mark running", err)
	}
	run.Status = domain.RunRunning
	s.startSupervision(entry)
	return nil
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
		err = s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunProvisioning, domain.RunAbandoned, "killed", killActor)
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
