package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// imageUserResolver is the optional runtime capability used to learn the
// user an image is configured to run as (*runtime.Docker implements it).
// Runtimes without it run images as their default user (root).
type imageUserResolver interface {
	ImageUser(ctx context.Context, ref string) (string, error)
}

// resolveContainerUser resolves the single numeric uid:gid a container
// and host-side ownership pass share: the profile override wins, an empty
// image user means root, a numeric image user is accepted, and a named
// image user without a profile mapping fails provisioning. Root resolves
// to "" (the image default) so Spec.User is only set when it matters.
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

// reserveCredentialUser atomically reserves a writable member home's
// non-root uid:gid. Runs that share a member home must use one mapping, so
// no ownership pass can race a live container using another mapping.
func (s *Scheduler) reserveCredentialUser(member domain.MemberID, user string, sharedHome bool, owner string, run *supervised) (*credentialUserReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncRunUserReservationsLocked()
	if !sharedHome || user == "" {
		if run != nil {
			run.runUser = user
		}
		return nil, nil
	}
	for other := range s.credentialUsers {
		if other.memberID != member || other.user == user {
			continue
		}
		return nil, fmt.Errorf("member's environment home %s is reserved by %s as user %s, but %s resolved user %s; concurrent containers for the same member must share one uid:gid mapping",
			member, other.owner, other.user, owner, user)
	}
	reservation := &credentialUserReservation{
		memberID: member,
		user:     user,
		owner:    owner,
		run:      run,
	}
	s.credentialUsers[reservation] = struct{}{}
	if run != nil {
		run.runUser = user
		run.userReservation = reservation
	}
	return reservation, nil
}

// syncRunUserReservationsLocked folds recovered runs, and runs installed
// directly by tests, into the common registry. Stale run reservations are
// discarded after their run leaves the live-run registry.
func (s *Scheduler) syncRunUserReservationsLocked() {
	if s.credentialUsers == nil {
		s.credentialUsers = make(map[*credentialUserReservation]struct{})
	}
	for reservation := range s.credentialUsers {
		if reservation.run == nil {
			continue
		}
		if s.runs[reservation.run.runID] == reservation.run && reservation.run.runUser != "" {
			continue
		}
		if reservation.run.userReservation == reservation {
			reservation.run.userReservation = nil
		}
		delete(s.credentialUsers, reservation)
	}
	for _, entry := range s.runs {
		if entry.runUser == "" || entry.userReservation != nil {
			continue
		}
		reservation := &credentialUserReservation{
			memberID: entry.memberID,
			user:     entry.runUser,
			owner:    "live run " + string(entry.runID),
			run:      entry,
		}
		s.credentialUsers[reservation] = struct{}{}
		entry.userReservation = reservation
	}
}

func (s *Scheduler) releaseCredentialUser(reservation *credentialUserReservation) {
	if reservation == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.credentialUsers, reservation)
	if reservation.run != nil && reservation.run.userReservation == reservation {
		reservation.run.userReservation = nil
	}
}

// reserveRunUser records the resolved run user and reserves its writable
// member home for the full live-run registry lifetime.
func (s *Scheduler) reserveRunUser(entry *supervised, user string, sharedHome bool) error {
	_, err := s.reserveCredentialUser(entry.memberID, user, sharedHome, "live run "+string(entry.runID), entry)
	return err
}

// errKillRequested aborts provisioning when a Kill was accepted for the
// in-flight run; failProvisioning turns it into abandoned ("killed").
var errKillRequested = errors.New("scheduler: kill requested during provisioning")

// checkFreeSpace applies the free-space floor (§ failure table, "Disk
// pressure"). It runs before the run row exists so a refusal leaves
// nothing behind, and it reads the filesystem holding the state directory,
// which is the same one the checkouts, transcripts and event log are on.
// A filesystem that cannot be read is not treated as full: the floor
// exists to stop a disk from filling, not to stop the server.
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

// checkAgentPresent refuses a neutral-image launch when the member home does
// not contain the executable expected by the resolved shipped profile.
func (s *Scheduler) checkAgentPresent(_ context.Context, member domain.MemberID, ws *domain.Workspace, harnessName, executable string) error {
	installScript := ""
	if p, ok := harness.Lookup(harnessName); ok {
		installScript = p.InstallScript
	}
	return s.checkAgentPresentWithScript(member, ws, harnessName, executable, installScript)
}

func (s *Scheduler) checkAgentPresentWithScript(member domain.MemberID, ws *domain.Workspace, harnessName, executable, installScript string) error {
	if _, deployment := s.harnesses[harnessName]; deployment {
		return nil
	}
	if ws == nil || !ws.Environment.NeutralImage || s.cfg.Homes == nil {
		return nil
	}
	return s.agentPresenceError(member, harnessName, executable, installScript)
}

func (s *Scheduler) memberHomeExecutable(member domain.MemberID, executable string) (bool, error) {
	if s.cfg.Homes == nil {
		return false, nil
	}
	homePath, err := s.cfg.Homes.Path(member)
	if err != nil {
		return false, fmt.Errorf("scheduler: resolve member home: %w", err)
	}
	root, err := os.OpenRoot(homePath)
	if err != nil {
		return false, fmt.Errorf("scheduler: open member home: %w", err)
	}
	defer func() { _ = root.Close() }()
	rel := filepath.Join(".local", "bin", executable)
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("scheduler: stat member home executable: %w", err)
	}
	// Vendor installers commonly leave ~/.local/bin/<exe> as a symlink to
	// a container-absolute versioned path (claude's native install). That
	// target only resolves inside the container where the home is mounted
	// at $HOME, so a symlink counts as installed without following it.
	if info.Mode()&fs.ModeSymlink != 0 {
		return true, nil
	}
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0, nil
}

func (s *Scheduler) agentPresenceError(member domain.MemberID, harnessName, executable, installScript string) error {
	present, err := s.memberHomeExecutable(member, executable)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	if installScript == "" {
		installScript = "install " + harnessName + " into ~/.local/bin"
	}
	return fmt.Errorf("scheduler: agent %q is not installed in your environment: %q is not in ~/.local/bin; open your terminal (aether terminal) and run: %s",
		harnessName, executable, installScript)
}

// Launch creates a new run and provisions it synchronously: checkout and
// branch via the git seam, container via the runtime, agent PTY via the
// PTY seam. It returns the run in running state, or an error with the run
// marked failed ("provisioning: <err>") once the row exists.
func (s *Scheduler) Launch(ctx context.Context, workspace domain.WorkspaceID, member domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	if mode == "" {
		mode = domain.LaunchTUI
	}
	if err := s.checkFreeSpace(); err != nil {
		return nil, err
	}
	argv, profile, err := s.command(ctx, member, harness, mode, task)
	if err != nil {
		return nil, err
	}
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, err
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	if err := s.checkAgentPresentWithScript(member, ws, harness, argv[0], profile.InstallScript); err != nil {
		return nil, err
	}
	run := &domain.Run{
		WorkspaceID: workspace,
		MemberID:    member,
		Task:        task,
		Harness:     harness,
		Mode:        mode,
		Status:      domain.RunQueued,
	}
	if err := s.cfg.Store.CreateRun(ctx, run); err != nil {
		return nil, err
	}
	if err := s.provision(ctx, run, ws, m, argv, profile, false); err != nil {
		return nil, err
	}
	return s.freshen(ctx, run), nil
}

// provision drives queued -> provisioning -> running. The run is
// registered in s.runs before any provisioning I/O so a concurrent Kill
// takes the supervised path (killRequested flag) instead of transitioning
// the row underneath the in-flight launch. Any error after the row exists
// marks the run failed ("provisioning: <err>"), or abandoned ("killed")
// when a kill was accepted meanwhile.
func (s *Scheduler) provision(ctx context.Context, run *domain.Run, ws *domain.Workspace, member *domain.Member, argv []string, profile harness.Profile, reuseCheckout bool) error {
	actor := member.ID
	entry := &supervised{
		runID:       run.ID,
		workspaceID: run.WorkspaceID,
		task:        run.Task,
		memberID:    run.MemberID,
		harness:     run.Harness,
		status:      domain.RunProvisioning,
		startedAt:   time.Now().UTC(),
	}
	s.mu.Lock()
	err := s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunQueued, domain.RunProvisioning, "", actor)
	if err == nil {
		s.runs[run.ID] = entry
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	run.Status = domain.RunProvisioning
	if err := s.provisionSteps(ctx, entry, run, ws, member, argv, profile, reuseCheckout); err != nil {
		s.failProvisioning(run, actor, err)
		return errors.New(publicRunStatusReason("provisioning: " + err.Error()))
	}
	return nil
}

func (s *Scheduler) provisionSteps(ctx context.Context, entry *supervised, run *domain.Run, ws *domain.Workspace, member *domain.Member, argv []string, profile harness.Profile, reuseCheckout bool) error {
	actor := member.ID
	if !reuseCheckout {
		checkout, branch, err := s.cfg.Git.CreateRunCheckout(ctx, ws.ID, run.ID, ws.BaseBranch, run.Task)
		if err != nil {
			return fmt.Errorf("create checkout: %w", err)
		}
		run.Worktree, run.Branch = checkout, branch
		if err := s.cfg.Store.UpdateRun(ctx, run); err != nil {
			return fmt.Errorf("record checkout: %w", err)
		}
	}
	if err := s.pinLatestProfile(ctx, run); err != nil {
		return fmt.Errorf("pin profile: %w", err)
	}
	plan, err := s.BuildEnvironmentPlan(ctx, run, ws, member, profile, EnvironmentPurposeRun)
	if err != nil {
		return err
	}
	if reserveErr := s.reserveRunUser(entry, plan.User, len(plan.Mounts) > 0); reserveErr != nil {
		return reserveErr
	}
	if ownErr := s.applyRunOwnership(ws, run, plan.Mounts, plan.User); ownErr != nil {
		return fmt.Errorf("apply run ownership: %w", ownErr)
	}
	// Coordination assets are Aether-owned container surfaces and are appended
	// after the environment plan's validated workspace mounts.
	coordMounts, mcpArgs := s.coordinationMounts(ctx, entry, run, profile)
	plan.Mounts = append(plan.Mounts, coordMounts...)
	argv = append(argv, mcpArgs...)
	cid, err := s.cfg.Runtime.Create(ctx, s.containerSpec(run, member, argv, plan))
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	fail := func(step string, cause error) error {
		if derr := s.cfg.Runtime.Destroy(context.WithoutCancel(ctx), cid); derr != nil {
			slog.Warn("scheduler: destroy container after failed provisioning", "run", run.ID, "error", derr)
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
	if perr := s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(run.ID), att); perr != nil {
		_ = att.Close()
		return fail("start pty session", perr)
	}
	if derr := s.cfg.Git.StartDiffWatch(ctx, run.WorkspaceID, run.ID); derr != nil {
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), ptyhost.RunSession(run.ID))
		return fail("start diff watch", derr)
	}
	s.mu.Lock()
	if entry.killRequested {
		err = errKillRequested
	} else {
		err = s.transitionLocked(ctx, run.ID, run.WorkspaceID, domain.RunProvisioning, domain.RunRunning, "", actor)
	}
	s.mu.Unlock()
	if err != nil {
		s.cfg.Git.StopDiffWatch(run.ID)
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), ptyhost.RunSession(run.ID))
		return fail("mark running", err)
	}
	run.Status = domain.RunRunning
	s.wg.Add(1)
	go s.superviseWait(entry)
	return nil
}

// failProvisioning records the terminal state after a provisioning error -
// abandoned ("killed") when a kill was accepted during provisioning,
// failed ("provisioning: <err>") otherwise - on a fresh context so a
// cancelled launch still lands in a consistent state.
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
		if _, cerr := s.cfg.Git.CommitAll(ctx, run.ID, "wip: "+taskLine(run.Task)); cerr != nil {
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
	delete(s.runs, run.ID)
	s.mu.Unlock()
	if err != nil {
		slog.Warn("scheduler: record provisioning outcome", "run", run.ID, "error", err)
	}
	s.removeSidecar(run.ID)
}

// freshen re-reads the run so callers see the row exactly as persisted
// (StartedAt, and any transition that raced the return).
func (s *Scheduler) freshen(ctx context.Context, run *domain.Run) *domain.Run {
	if fresh, err := s.cfg.Store.GetRun(ctx, run.ID); err == nil {
		return fresh
	}
	return run
}
