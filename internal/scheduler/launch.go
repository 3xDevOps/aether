package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
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

// credentialMounts builds and validates the member's read-write harness
// credential-home mounts (<HomesDir>/<member>/<harness> mirroring the
// profile's home-relative credential paths beneath the run user's
// container home), creating host directories on first use. Empty HomesDir
// disables credential homes.
func (s *Scheduler) credentialMounts(run *domain.Run, member domain.MemberID, profile harness.Profile, containerHome string) ([]runtime.Mount, error) {
	if s.cfg.HomesDir == "" {
		return nil, nil
	}
	home := filepath.Join(s.cfg.HomesDir, string(member), profile.Name)
	mounts := profile.CredentialMounts(home, containerHome)
	if len(mounts) == 0 {
		return nil, nil
	}
	for _, m := range mounts {
		// A credential path may be a regular file (auth.json at the top of
		// home); precreate only what does not exist, as an empty directory.
		// An existing file must not be MkdirAll'd into an error.
		if _, statErr := os.Lstat(m.HostPath); statErr == nil {
			continue
		}
		if err := os.MkdirAll(m.HostPath, 0o700); err != nil {
			return nil, fmt.Errorf("create credential home: %w", err)
		}
	}
	// Containment is the member's own harness home, not all of HomesDir:
	// a symlink planted during setup must not alias another member's (or
	// another harness's) files into this run.
	if err := runtime.ValidateMounts(mounts, runtime.MountPolicy{
		OwnedRoots:        []string{home},
		WorktreeHostPath:  run.Worktree,
		WorktreeMountPath: s.cfg.WorktreeMount,
	}); err != nil {
		return nil, err
	}
	return mounts, nil
}

// reserveCredentialUser atomically reserves a writable credential home's
// non-root uid:gid. Runs and setup sessions use the same registry, so no
// ownership pass can race a live container using another mapping.
func (s *Scheduler) reserveCredentialUser(member domain.MemberID, harnessName, user string, sharedHome bool, owner string, run *supervised) (*credentialUserReservation, error) {
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
		if other.memberID != member || other.harness != harnessName || other.user == user {
			continue
		}
		return nil, fmt.Errorf("credential home %s/%s is reserved by %s as user %s, but %s resolved user %s; concurrent runs and setup sessions for the same member/harness must share one uid:gid mapping",
			member, harnessName, other.owner, other.user, owner, user)
	}
	reservation := &credentialUserReservation{
		memberID: member,
		harness:  harnessName,
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
			harness:  entry.harness,
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
// credential home for the full live-run registry lifetime.
func (s *Scheduler) reserveRunUser(entry *supervised, user string, sharedHome bool) error {
	_, err := s.reserveCredentialUser(entry.memberID, entry.harness, user, sharedHome, "live run "+string(entry.runID), entry)
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

// Launch creates a new run and provisions it synchronously: checkout and
// branch via the git seam, container via the runtime, agent PTY via the
// PTY seam. It returns the run in running state, or an error with the run
// marked failed ("provisioning: <err>") once the row exists.
func (s *Scheduler) Launch(ctx context.Context, session domain.SessionID, member domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
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
	sess, err := s.cfg.Store.GetSession(ctx, session)
	if err != nil {
		return nil, err
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, sess.WorkspaceID)
	if err != nil {
		return nil, err
	}
	run := &domain.Run{
		SessionID: session,
		MemberID:  member,
		Task:      task,
		Harness:   harness,
		Mode:      mode,
		Status:    domain.RunQueued,
	}
	if err := s.cfg.Store.CreateRun(ctx, run); err != nil {
		return nil, err
	}
	if err := s.provision(ctx, run, sess, ws, m, argv, profile, false); err != nil {
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
func (s *Scheduler) provision(ctx context.Context, run *domain.Run, sess *domain.Session, ws *domain.Workspace, member *domain.Member, argv []string, profile harness.Profile, reuseCheckout bool) error {
	actor := member.ID
	entry := &supervised{
		runID:       run.ID,
		sessionID:   run.SessionID,
		workspaceID: ws.ID,
		task:        run.Task,
		memberID:    run.MemberID,
		harness:     run.Harness,
		status:      domain.RunProvisioning,
		startedAt:   time.Now().UTC(),
	}
	s.mu.Lock()
	err := s.transitionLocked(ctx, run.ID, run.SessionID, domain.RunQueued, domain.RunProvisioning, "", actor)
	if err == nil {
		s.runs[run.ID] = entry
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	run.Status = domain.RunProvisioning
	if err := s.provisionSteps(ctx, entry, run, sess, ws, member, argv, profile, reuseCheckout); err != nil {
		s.failProvisioning(run, actor, err)
		return errors.New(publicRunStatusReason("provisioning: " + err.Error()))
	}
	return nil
}

func (s *Scheduler) provisionSteps(ctx context.Context, entry *supervised, run *domain.Run, sess *domain.Session, ws *domain.Workspace, member *domain.Member, argv []string, profile harness.Profile, reuseCheckout bool) error {
	actor := member.ID
	if !reuseCheckout {
		checkout, branch, err := s.cfg.Git.CreateRunCheckout(ctx, ws.ID, run.ID, sess.BaseBranch, run.Task)
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
	plan, err := s.BuildEnvironmentPlan(ctx, run, ws, member, profile, EnvironmentPurposeRun, "")
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
	if perr := s.cfg.PTY.StartSession(ctx, run.ID, att); perr != nil {
		_ = att.Close()
		return fail("start pty session", perr)
	}
	if derr := s.cfg.Git.StartDiffWatch(ctx, run.SessionID, run.ID); derr != nil {
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), run.ID)
		return fail("start diff watch", derr)
	}
	s.mu.Lock()
	if entry.killRequested {
		err = errKillRequested
	} else {
		err = s.transitionLocked(ctx, run.ID, run.SessionID, domain.RunProvisioning, domain.RunRunning, "", actor)
	}
	s.mu.Unlock()
	if err != nil {
		s.cfg.Git.StopDiffWatch(run.ID)
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), run.ID)
		return fail("mark running", err)
	}
	run.Status = domain.RunRunning
	s.wg.Add(1)
	go s.superviseWait(entry)
	return nil
}

// failProvisioning records the terminal state after a provisioning error —
// abandoned ("killed") when a kill was accepted during provisioning,
// failed ("provisioning: <err>") otherwise — on a fresh context so a
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
		err = s.transitionLocked(ctx, run.ID, run.SessionID, domain.RunProvisioning, domain.RunAbandoned, "killed", killActor)
	} else {
		err = s.transitionLocked(ctx, run.ID, run.SessionID, domain.RunProvisioning, domain.RunFailed,
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
