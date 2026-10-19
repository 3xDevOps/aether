package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	terminalTabMain = "main"
	maxTerminalTabs = 6
	// terminalUnknownUser is a reservation sentinel, never a container
	// identity: failed metadata inspection must not be treated as root.
	terminalUnknownUser = "<unknown-terminal-user>"
	// Terminal cleanup runs after the main shell exits and must not block a
	// member lock forever if the daemon or store is unavailable.
	terminalCleanupTimeout = 30 * time.Second
)

var (
	ErrInvalidTerminalTab = errors.New("terminal: invalid tab")
	ErrTerminalTabLimit   = errors.New("terminal: at most 6 tabs")
	terminalTabPattern    = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

type terminalSupervision struct {
	member           domain.MemberID
	containerID      runtime.ID
	image            string
	startedAt        time.Time
	home             string
	runUser          string
	userReservation  *credentialUserReservation
	metadataPending  bool
	ownershipBlocked bool
	persistPending   bool
	// cleanupPending keeps the registry and container identity in place when
	// an exited terminal cannot yet be destroyed or its row removed.
	cleanupPending bool
}

func terminalContainerName(member domain.MemberID) string {
	return "aether-terminal-" + string(member)
}

func terminalCreationKey(member domain.MemberID) string {
	return "terminal:" + string(member)
}

func terminalPrefix(member domain.MemberID) string {
	return "terminal:" + string(member) + ":"
}

func (s *Scheduler) terminalLock(member domain.MemberID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalLocks == nil {
		s.terminalLocks = make(map[domain.MemberID]*sync.Mutex)
	}
	lock := s.terminalLocks[member]
	if lock == nil {
		lock = &sync.Mutex{}
		s.terminalLocks[member] = lock
	}
	return lock
}

// TerminalContainerAddr returns the network address of a supervised member
// terminal container.
func (s *Scheduler) TerminalContainerAddr(ctx context.Context, member domain.MemberID) (string, error) {
	entry, cleanupPending := s.terminalSnapshot(member)
	if entry == nil || cleanupPending {
		return "", errors.New("environment terminal is not running")
	}
	return s.cfg.Runtime.ContainerIP(ctx, entry.containerID)
}

// EnsureTerminal creates or adopts one long-lived terminal container for a member.
func (s *Scheduler) EnsureTerminal(ctx context.Context, member domain.MemberID) (*domain.Terminal, error) {
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()
	return s.ensureTerminalAdmittedLocked(ctx, member)
}

// The caller holds terminalLock. Only disk-pressure reclamation releases it:
// provisioning is then restarted under the lock, rechecking member existence
// and terminal ownership before admission. The reclaim pass keeps the kept
// container being opened. Existing terminals need no admission.
func (s *Scheduler) ensureTerminalAdmittedLocked(ctx context.Context, member domain.MemberID) (*domain.Terminal, error) {
	terminal, err := s.ensureTerminalLocked(ctx, member)
	if !errors.Is(err, ErrDiskFull) || ctx.Err() != nil {
		return terminal, err
	}
	lock := s.terminalLock(member)
	lock.Unlock()
	s.reclaimDisk(ctx, member)
	lock.Lock()
	return s.ensureTerminalLocked(ctx, member)
}

func (s *Scheduler) ensureTerminalLocked(ctx context.Context, member domain.MemberID) (*domain.Terminal, error) {
	// Lock order: terminalLock -> cache lock -> short Scheduler.mu snapshots.
	// Keep it through durable publication, including creation-key adoption.
	if s.cfg.Homes != nil {
		unlock := s.cfg.Homes.LockCaches(member)
		defer unlock()
		defer func() { _ = s.cfg.Homes.TouchCache(member, "terminal") }()
	}
	if existing, cleanupPending := s.terminalSnapshot(member); existing != nil {
		if cleanupPending {
			if err := s.cleanupExitedTerminalLocked(ctx, existing); err != nil {
				return nil, err
			}
		} else {
			terminal := terminalFromSupervision(existing)
			if err := s.ensureTerminalReady(ctx, existing, terminal); err != nil {
				return nil, err
			}
			return terminal, nil
		}
	}
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, fmt.Errorf("scheduler: get terminal member: %w", err)
	}
	row, rowErr := s.cfg.Store.GetTerminal(ctx, member)
	if rowErr != nil && !errors.Is(rowErr, store.ErrNotFound) {
		return nil, fmt.Errorf("scheduler: get terminal record: %w", rowErr)
	}

	key := terminalCreationKey(member)
	cid, findErr := s.cfg.Runtime.FindByCreationKey(ctx, key)
	if findErr == nil {
		adopted, adoptedOK, adoptErr := s.tryAdoptTerminal(ctx, m, row, cid)
		if adoptedOK {
			return s.finishTerminalAdoption(ctx, adopted)
		}
		if adoptErr != nil {
			return nil, adoptErr
		}
	} else if !errors.Is(findErr, runtime.ErrNotFound) {
		return nil, fmt.Errorf("scheduler: find terminal container: %w", findErr)
	}
	if row != nil && (findErr != nil || row.ContainerID != string(cid)) {
		adopted, adoptedOK, adoptErr := s.tryAdoptTerminal(ctx, m, row, runtime.ID(row.ContainerID))
		if adoptedOK {
			return s.finishTerminalAdoption(ctx, adopted)
		}
		if adoptErr != nil {
			return nil, adoptErr
		}
		if destroyErr := s.destroyTerminalContainer(ctx, runtime.ID(row.ContainerID)); destroyErr != nil {
			s.retainTerminalAfterRuntimeError(m, row, runtime.ID(row.ContainerID))
			return nil, fmt.Errorf("scheduler: destroy stale terminal: %w", destroyErr)
		}
	}
	var kept runtime.ID
	if findErr == nil {
		kept = cid
	}
	release, err := s.tryReserveCapacity(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	s.removeScratch(terminalScratchName(member))
	plan, err := s.BuildEnvironmentPlan(ctx, nil, nil, m, harness.Profile{}, EnvironmentPurposeTerminal)
	if err != nil {
		return nil, fmt.Errorf("scheduler: build terminal environment: %w", err)
	}
	coordMounts, coordErr := s.terminalCoordinationMount(member)
	if coordErr != nil {
		return nil, coordErr
	}
	plan.Mounts = append(plan.Mounts, coordMounts...)
	if len(coordMounts) > 0 {
		ensureCoordinationCLIPath(plan.Env)
	}
	terminalReservation := &terminalSupervision{member: member}
	if reserveErr := s.reserveTerminalUser(terminalReservation, plan.User); reserveErr != nil {
		s.releaseTerminalCoordination(member)
		return nil, fmt.Errorf("scheduler: reserve terminal user: %w", reserveErr)
	}
	if ownershipErr := s.applyRunOwnership(nil, &domain.Run{}, member, plan.Mounts, plan.User); ownershipErr != nil {
		s.releaseTerminalCoordination(member)
		s.releaseTerminalReservation(terminalReservation)
		return nil, fmt.Errorf("scheduler: apply terminal ownership: %w", ownershipErr)
	}
	startedAt := time.Now().UTC()
	spec := runtime.Spec{
		Name:       terminalContainerName(member),
		Image:      plan.Image,
		Env:        plan.Env,
		WorkingDir: plan.Home,
		// Init hides child exec failures from Start, so select the shell inside the container.
		Command:          []string{"/bin/sh", "-c", "if [ -x /bin/bash ]; then exec /bin/bash -l; else exec /bin/sh -l; fi"},
		TTY:              true,
		Mounts:           plan.Mounts,
		User:             plan.User,
		CreationKey:      key,
		CPULimit:         s.cfg.RunCPULimit,
		MemoryLimitBytes: s.cfg.RunMemoryBytes,
		PidsLimit:        s.cfg.RunPidsLimit,
		NestedDocker:     true,
	}
	spec.Fingerprint = terminalFingerprint(spec, s.cfg.StandardImage)
	cid, image, err := s.startOrCreateTerminal(ctx, kept, spec)
	if err != nil {
		if cid == "" {
			s.releaseTerminalCoordination(member)
		} else {
			if updateErr := s.updateTerminalCoordination(member, cid); updateErr != nil {
				slog.Warn("scheduler: retain terminal coordination container reference", "member", member, "container", cid, "error", updateErr)
			}
			s.retainTerminalAfterRuntimeError(m, nil, cid)
		}
		s.releaseTerminalReservation(terminalReservation)
		return nil, err
	}
	if len(coordMounts) > 0 {
		if updateErr := s.updateTerminalCoordination(member, cid); updateErr != nil {
			destroyErr := s.cfg.Runtime.Destroy(context.WithoutCancel(ctx), cid)
			if destroyErr == nil || errors.Is(destroyErr, runtime.ErrNotFound) {
				s.releaseTerminalCoordination(member)
			} else {
				s.retainTerminalAfterRuntimeError(m, nil, cid)
			}
			s.releaseTerminalReservation(terminalReservation)
			if destroyErr != nil && !errors.Is(destroyErr, runtime.ErrNotFound) {
				return nil, errors.Join(updateErr, fmt.Errorf("scheduler: destroy terminal after sidecar failure: %w", destroyErr))
			}
			return nil, updateErr
		}
	}
	terminalReservation.containerID = cid
	terminal := &domain.Terminal{Member: member, ContainerID: string(cid), Image: image, StartedAt: startedAt}
	sup := s.registerTerminal(terminal, terminalReservation.userReservation, plan.Home)
	sup.persistPending = true
	if err := s.ensureTerminalReady(ctx, sup, terminal); err != nil {
		return nil, err
	}
	return terminal, nil
}

// terminalFingerprint identifies the settings a terminal container was
// created from. The image is the standard image, not the member's: saving
// the environment changes the member's image without changing the kept
// container, and only the standard image can differ for a given container
// otherwise. Env values are hashed, never stored.
func terminalFingerprint(spec runtime.Spec, standardImage string) string {
	spec.Image, spec.Name, spec.CreationKey, spec.Fingerprint = standardImage, "", "", ""
	data, err := json.Marshal(spec)
	if err != nil {
		panic(fmt.Sprintf("scheduler: fingerprint terminal spec: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// startOrCreateTerminal starts the member's kept container when it was
// created from spec's settings, and otherwise replaces it with a fresh one.
// A kept container that fails to start is replaced too, so a broken container
// never forces a Reset that would also discard the saved image. It also
// returns the image the container was created from, which a kept container
// keeps across saves.
func (s *Scheduler) startOrCreateTerminal(ctx context.Context, kept runtime.ID, spec runtime.Spec) (runtime.ID, string, error) {
	if kept != "" {
		info, err := s.cfg.Runtime.Inspect(ctx, kept)
		switch {
		case err == nil && info.Fingerprint == spec.Fingerprint:
			startErr := s.cfg.Runtime.Start(ctx, kept)
			if startErr == nil {
				return kept, info.Image, nil
			}
			if ctx.Err() != nil {
				// A cancelled open says nothing about the container.
				return "", "", fmt.Errorf("scheduler: start kept terminal: %w", startErr)
			}
			slog.Warn("scheduler: kept terminal did not start; recreating it from the saved image", "container", kept, "error", startErr)
		case err != nil && !errors.Is(err, runtime.ErrNotFound):
			return "", "", fmt.Errorf("scheduler: inspect kept terminal: %w", err)
		}
		if err := s.destroyTerminalContainer(ctx, kept); err != nil {
			return "", "", fmt.Errorf("scheduler: replace kept terminal: %w", err)
		}
	}
	cid, err := s.createAndStartTerminal(ctx, spec)
	return cid, spec.Image, err
}

func (s *Scheduler) destroyTerminalContainer(ctx context.Context, cid runtime.ID) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalCleanupTimeout)
	defer cancel()
	if err := s.cfg.Runtime.Destroy(cleanupCtx, cid); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		return err
	}
	return nil
}

// terminalContainerRunning probes with a short Wait; its deadline means the
// main process is still running.
func (s *Scheduler) terminalContainerRunning(ctx context.Context, cid runtime.ID, timeout time.Duration) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := s.cfg.Runtime.Wait(probeCtx, cid)
	switch {
	case err == nil, errors.Is(err, runtime.ErrNotFound):
		return false, nil
	case ctx.Err() != nil:
		return false, ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return true, nil
	}
	return false, err
}

// DestroyStoppedTerminal destroys the member's kept terminal container when
// it is stopped, discarding its unsaved changes, and reports whether it did.
// A terminal whose lock is held is left alone.
func (s *Scheduler) DestroyStoppedTerminal(ctx context.Context, member domain.MemberID) (bool, error) {
	lock := s.terminalLock(member)
	if !lock.TryLock() {
		return false, nil
	}
	defer lock.Unlock()
	return s.destroyKeptTerminalLocked(ctx, member, true)
}

// destroyKeptTerminalLocked destroys the container found by the member's
// creation key. With onlyStopped, a supervised or running one is left alone.
func (s *Scheduler) destroyKeptTerminalLocked(ctx context.Context, member domain.MemberID, onlyStopped bool) (bool, error) {
	if onlyStopped && s.lookupLiveTerminal(member) != nil {
		return false, nil
	}
	cid, err := s.cfg.Runtime.FindByCreationKey(ctx, terminalCreationKey(member))
	if errors.Is(err, runtime.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("scheduler: find kept terminal: %w", err)
	}
	if onlyStopped {
		running, err := s.terminalContainerRunning(ctx, cid, s.cfg.ExitProbeTimeout)
		if err != nil {
			return false, fmt.Errorf("scheduler: probe kept terminal: %w", err)
		}
		if running {
			return false, nil
		}
	}
	if err := s.destroyTerminalContainer(ctx, cid); err != nil {
		return false, fmt.Errorf("scheduler: destroy kept terminal: %w", err)
	}
	return true, nil
}

func (s *Scheduler) createAndStartTerminal(ctx context.Context, spec runtime.Spec) (runtime.ID, error) {
	cid, err := s.cfg.Runtime.Create(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("scheduler: create terminal: %w", err)
	}
	if startErr := s.cfg.Runtime.Start(ctx, cid); startErr == nil {
		return cid, nil
	} else {
		destroyErr := s.cfg.Runtime.Destroy(context.Background(), cid)
		if destroyErr != nil && !errors.Is(destroyErr, runtime.ErrNotFound) {
			return cid, errors.Join(
				fmt.Errorf("scheduler: start terminal: %w", startErr),
				fmt.Errorf("scheduler: destroy terminal after start failure: %w", destroyErr),
			)
		}
		return "", fmt.Errorf("scheduler: start terminal: %w", startErr)
	}
}

func (s *Scheduler) lookupTerminal(member domain.MemberID) *terminalSupervision {
	sup, _ := s.terminalSnapshot(member)
	return sup
}

// terminalSnapshot returns the supervision entry and its cleanup state from
// one Scheduler.mu-protected snapshot. A cleanup-pending entry remains in the
// registry so its container identity can be retried safely, but it is not a
// live terminal.
func (s *Scheduler) terminalSnapshot(member domain.MemberID) (*terminalSupervision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sup := s.terminals[member]
	if sup == nil {
		return nil, false
	}
	return sup, sup.cleanupPending
}

func (s *Scheduler) lookupLiveTerminal(member domain.MemberID) *terminalSupervision {
	sup, cleanupPending := s.terminalSnapshot(member)
	if cleanupPending {
		return nil
	}
	return sup
}

func (s *Scheduler) registerAdoptedTerminal(adopted *terminalAdoption) *terminalSupervision {
	sup := s.registerTerminal(adopted.terminal, adopted.reservation, adopted.home)
	s.mu.Lock()
	sup.metadataPending = adopted.metadataPending
	sup.ownershipBlocked = adopted.ownershipBlocked
	sup.persistPending = adopted.persistPending
	s.mu.Unlock()
	return sup
}

func (s *Scheduler) registerTerminal(terminal *domain.Terminal, reservation *credentialUserReservation, home string) *terminalSupervision {
	sup := &terminalSupervision{
		member: terminal.Member, containerID: runtime.ID(terminal.ContainerID),
		image: terminal.Image, startedAt: terminal.StartedAt, home: home,
	}
	if reservation != nil {
		sup.runUser = reservation.user
		sup.userReservation = reservation
	}
	s.mu.Lock()
	if s.terminals == nil {
		s.terminals = make(map[domain.MemberID]*terminalSupervision)
	}
	if old := s.terminals[terminal.Member]; old != nil {
		if reservation != nil {
			reservation.terminal = nil
			delete(s.credentialUsers, reservation)
		}
		s.mu.Unlock()
		return old
	}
	if reservation != nil {
		reservation.terminal = sup
		reservation.pending = false
	}
	s.terminals[terminal.Member] = sup
	s.mu.Unlock()
	s.wg.Add(1)
	go s.superviseTerminal(sup)
	return sup
}

func terminalFromSupervision(sup *terminalSupervision) *domain.Terminal {
	return &domain.Terminal{
		Member: sup.member, ContainerID: string(sup.containerID),
		Image: sup.image, StartedAt: sup.startedAt,
	}
}

func (s *Scheduler) finishTerminalAdoption(ctx context.Context, adopted *terminalAdoption) (*domain.Terminal, error) {
	sup := s.registerAdoptedTerminal(adopted)
	terminal := terminalFromSupervision(sup)
	if err := s.ensureTerminalReady(ctx, sup, terminal); err != nil {
		return nil, err
	}
	return terminal, nil
}

// ensureTerminalReady repairs a survivor that was registered before its
// attach, metadata, or durable row handoff completed. The member lock held by
// the caller serializes these retries and the registry keeps ownership safe
// between calls.
func (s *Scheduler) ensureTerminalReady(ctx context.Context, sup *terminalSupervision, terminal *domain.Terminal) error {
	if sup.metadataPending {
		info, err := s.cfg.Runtime.Inspect(ctx, sup.containerID)
		if err != nil {
			return fmt.Errorf("scheduler: inspect terminal container: %w", err)
		}
		user, home, err := terminalContainerMetadata(info)
		if err != nil {
			return err
		}
		if err := s.resolveTerminalMetadata(sup, user, home); err != nil {
			return err
		}
		if info.Image != "" {
			s.mu.Lock()
			sup.image = info.Image
			terminal.Image = info.Image
			s.mu.Unlock()
		}
	}
	if sup.ownershipBlocked {
		if err := s.checkTerminalOwnership(sup); err != nil {
			return err
		}
		s.mu.Lock()
		sup.ownershipBlocked = false
		s.mu.Unlock()
	}
	if !s.hasTerminalSession(sup.member, terminalTabMain) {
		if err := s.attachTerminalSession(ctx, sup.member, sup.containerID); err != nil {
			return fmt.Errorf("scheduler: attach terminal: %w", err)
		}
	}
	if sup.persistPending {
		if err := s.cfg.Store.PutTerminal(ctx, terminal); err != nil {
			return fmt.Errorf("scheduler: persist terminal: %w", err)
		}
		s.mu.Lock()
		sup.persistPending = false
		s.mu.Unlock()
	}
	return nil
}

func (s *Scheduler) resolveTerminalMetadata(sup *terminalSupervision, user, home string) error {
	s.mu.Lock()
	reservation := sup.userReservation
	if reservation == nil {
		s.mu.Unlock()
		s.retainTerminalReservation(sup, user)
		s.mu.Lock()
		reservation = sup.userReservation
	}
	sup.home = home
	sup.metadataPending = false
	if reservation != nil {
		reservation.user = user
		sup.runUser = user
	}
	for other := range s.credentialUsers {
		if other != reservation && other.blocks(sup.member, "", user) {
			sup.ownershipBlocked = true
			s.mu.Unlock()
			return terminalOwnershipConflict(sup.member, other, user)
		}
	}
	sup.ownershipBlocked = false
	if user == "" {
		if reservation != nil {
			delete(s.credentialUsers, reservation)
		}
		sup.userReservation = nil
		sup.runUser = ""
	} else if reservation != nil {
		reservation.pending = false
	}
	s.mu.Unlock()
	return nil
}
func (s *Scheduler) checkTerminalOwnership(sup *terminalSupervision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for other := range s.credentialUsers {
		if other != sup.userReservation && other.blocks(sup.member, "", sup.runUser) {
			return terminalOwnershipConflict(sup.member, other, sup.runUser)
		}
	}
	return nil
}

func terminalOwnershipConflict(member domain.MemberID, other *credentialUserReservation, user string) error {
	return fmt.Errorf("member's environment home %s is reserved by %s as user %s, but environment terminal resolved user %s; concurrent containers for the same member must share one uid:gid mapping",
		member, other.owner, other.user, user)
}

func (s *Scheduler) retainTerminalReservation(entry *terminalSupervision, user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.userReservation != nil {
		return
	}
	s.syncRunUserReservationsLocked()
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
}

func (s *Scheduler) releaseTerminalReservation(entry *terminalSupervision) {
	if entry == nil {
		return
	}
	s.mu.Lock()
	if reservation := entry.userReservation; reservation != nil {
		delete(s.credentialUsers, reservation)
		entry.userReservation = nil
	}
	entry.runUser = ""
	s.mu.Unlock()
}

type terminalAdoption struct {
	terminal         *domain.Terminal
	home             string
	reservation      *credentialUserReservation
	metadataPending  bool
	ownershipBlocked bool
	persistPending   bool
}

// tryAdoptTerminal adopts cid when its main process is still running. An
// exited container is left in place: the caller either starts it again or
// replaces it, depending on its fingerprint. The probe mirrors
// recoverSupervised: a short non-destructive Wait whose deadline means
// "still running".
func (s *Scheduler) tryAdoptTerminal(ctx context.Context, member *domain.Member, row *domain.Terminal, cid runtime.ID) (*terminalAdoption, bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, s.cfg.ExitProbeTimeout)
	_, waitErr := s.cfg.Runtime.Wait(probeCtx, cid)
	cancel()
	switch {
	case waitErr == nil, errors.Is(waitErr, runtime.ErrNotFound):
		return nil, false, nil
	case errors.Is(waitErr, context.DeadlineExceeded):
		// Still running: adopt it.
	case ctx.Err() != nil:
		return nil, false, ctx.Err()
	default:
		s.retainTerminalAfterRuntimeError(member, row, cid)
		return nil, false, fmt.Errorf("scheduler: probe terminal container: %w", waitErr)
	}
	return s.pendingTerminalAdoption(member, row, cid), true, nil
}

// pendingTerminalAdoption captures ownership before runtime metadata, PTY
// attach, or durable persistence is repaired. The reservation is deliberately
// unknown until Inspect succeeds; treating an inconclusive probe as root would
// allow another container to take ownership of the member home.
func (s *Scheduler) pendingTerminalAdoption(member *domain.Member, row *domain.Terminal, cid runtime.ID) *terminalAdoption {
	terminal := terminalForAdoption(member, row, cid, "")
	if terminal.Image == "" {
		terminal.Image = s.cfg.StandardImage
	}
	entry := &terminalSupervision{
		member: member.ID, containerID: cid, image: terminal.Image,
		startedAt: terminal.StartedAt, metadataPending: true,
	}
	s.retainTerminalReservation(entry, terminalUnknownUser)
	return &terminalAdoption{
		terminal: terminal, home: "", reservation: entry.userReservation,
		metadataPending: true, persistPending: row == nil || row.ContainerID != string(cid),
	}
}

func (s *Scheduler) retainTerminalAfterRuntimeError(member *domain.Member, row *domain.Terminal, cid runtime.ID) {
	s.registerAdoptedTerminal(s.pendingTerminalAdoption(member, row, cid))
}

func terminalForAdoption(member *domain.Member, row *domain.Terminal, cid runtime.ID, image string) *domain.Terminal {
	terminal := &domain.Terminal{
		Member: member.ID, ContainerID: string(cid), Image: image, StartedAt: time.Now().UTC(),
	}
	if row != nil {
		if terminal.Image == "" {
			terminal.Image = row.Image
		}
		terminal.StartedAt = row.StartedAt
	}
	if terminal.Image == "" {
		terminal.Image = member.Image
	}
	return terminal
}

func terminalContainerMetadata(info runtime.ContainerInfo) (string, string, error) {
	var home string
	for _, value := range info.Env {
		name, candidate, ok := strings.Cut(value, "=")
		if ok && name == "HOME" {
			home = candidate
			break
		}
	}
	if home == "" || !strings.HasPrefix(home, "/") {
		return "", "", errors.New("scheduler: adopted terminal has no absolute HOME")
	}
	user := info.User
	if user == "0" || user == "0:0" {
		user = ""
	}
	return user, home, nil
}
func (s *Scheduler) hasTerminalSession(member domain.MemberID, tab string) bool {
	key := ptyhost.TerminalSession(member, tab)
	return slices.Contains(s.cfg.PTY.ActiveSessions(terminalPrefix(member)), key)
}

func (s *Scheduler) attachTerminalSession(ctx context.Context, member domain.MemberID, cid runtime.ID) error {
	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err != nil {
		return err
	}
	if s.hasTerminalSession(member, terminalTabMain) {
		_ = att.Close()
		return nil
	}
	if sessionErr := s.cfg.PTY.StartSession(ctx, ptyhost.TerminalSession(member, terminalTabMain), att); sessionErr != nil {
		_ = att.Close()
		return fmt.Errorf("scheduler: start terminal session: %w", sessionErr)
	}
	return nil
}

// cleanupExitedTerminalLocked unregisters an exited terminal once its matching
// durable row is handled. The container stays, stopped, for the next open. The
// supervision entry stays registered while the row can be retried, so a later
// Ensure cannot create a replacement that an old supervisor would delete.
func (s *Scheduler) cleanupExitedTerminalLocked(ctx context.Context, sup *terminalSupervision) error {
	if sup == nil {
		return nil
	}
	s.mu.Lock()
	if s.terminals[sup.member] != sup {
		s.mu.Unlock()
		return nil
	}
	sup.cleanupPending = true
	s.mu.Unlock()
	s.stopSupervisionGitHubOAuth(sup.member, sup)

	cleanupCtx, cancel := context.WithTimeout(ctx, terminalCleanupTimeout)
	defer cancel()
	s.cfg.PTY.StopSessionsWithPrefix(cleanupCtx, terminalPrefix(sup.member))

	row, err := s.cfg.Store.GetTerminal(cleanupCtx, sup.member)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("scheduler: get exited terminal record: %w", err)
	}
	// A replacement may have persisted a new row while cleanup was in
	// progress. Never delete a row naming another container.
	if row != nil && row.ContainerID == string(sup.containerID) {
		if err := s.cfg.Store.DeleteTerminal(cleanupCtx, sup.member); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("scheduler: delete exited terminal record: %w", err)
		}
	}

	s.mu.Lock()
	owned := s.terminals[sup.member] == sup
	if owned {
		delete(s.terminals, sup.member)
	}
	s.mu.Unlock()
	// The staged CLI is released once the matching durable terminal row is
	// handled; the next open stages it again before starting the container.
	s.releaseTerminalCoordination(sup.member)
	// The reservation belongs to this supervision, not to whichever
	// replacement may now occupy the member slot. Release it even when the
	// slot changed so a failed cleanup cannot strand an ownership claim.
	s.releaseTerminalReservation(sup)
	return nil
}

func (s *Scheduler) superviseTerminal(sup *terminalSupervision) {
	defer s.wg.Done()
	_, waitErr := waitForExit(s.superCtx, func(ctx context.Context) (runtime.ExitStatus, error) {
		return s.cfg.Runtime.Wait(ctx, sup.containerID)
	})
	if s.superCtx.Err() != nil {
		return
	}
	if waitErr != nil && !errors.Is(waitErr, runtime.ErrNotFound) {
		slog.Warn("scheduler: terminal wait inconclusive; retaining supervision", "member", sup.member, "container", sup.containerID, "error", waitErr)
		return
	}
	// The main process exited (or the container disappeared). Clean up under
	// the member lock so a concurrent Ensure or Stop never observes a
	// half-cleaned terminal, and only while this supervision owns the entry.
	lock := s.terminalLock(sup.member)
	lock.Lock()
	defer lock.Unlock()
	s.mu.Lock()
	owned := s.terminals[sup.member] == sup
	s.mu.Unlock()
	if !owned {
		return
	}
	if err := s.cleanupExitedTerminalLocked(context.Background(), sup); err != nil {
		slog.Warn("scheduler: clean up exited terminal", "member", sup.member, "container", sup.containerID, "error", err)
	} else {
		s.touchTerminalCache(sup.member)
	}
}

// EnsureTerminalTab ensures a terminal tab process exists for a member.
func (s *Scheduler) EnsureTerminalTab(ctx context.Context, member domain.MemberID, tab string, cols, rows uint) error {
	if tab == "" {
		tab = terminalTabMain
	}
	if tab != terminalTabMain && !terminalTabPattern.MatchString(tab) {
		return ErrInvalidTerminalTab
	}
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()
	terminal, err := s.ensureTerminalAdmittedLocked(ctx, member)
	if err != nil {
		return err
	}
	if s.hasTerminalSession(member, tab) {
		return nil
	}
	if len(s.cfg.PTY.ActiveSessions(terminalPrefix(member))) >= maxTerminalTabs {
		return ErrTerminalTabLimit
	}
	memberRow, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return fmt.Errorf("scheduler: get terminal tab member: %w", err)
	}
	unlockCaches := func() {}
	if s.cfg.Homes != nil {
		unlockCaches = s.cfg.Homes.LockCaches(member)
	}
	plan, err := s.BuildEnvironmentPlan(ctx, nil, nil, memberRow, harness.Profile{}, EnvironmentPurposeTerminal)
	unlockCaches()
	if err != nil {
		return fmt.Errorf("scheduler: build terminal tab environment: %w", err)
	}
	argv := []string{"/bin/bash", "-l"}
	att, err := s.cfg.Runtime.ExecTTY(ctx, runtime.ID(terminal.ContainerID), argv, plan.Home, cols, rows)
	if err != nil {
		var exitErr *runtime.ExecExitError
		if !errors.As(err, &exitErr) || (exitErr.Code != 126 && exitErr.Code != 127) {
			return fmt.Errorf("scheduler: exec terminal tab %q: %w", tab, err)
		}
		att, err = s.cfg.Runtime.ExecTTY(ctx, runtime.ID(terminal.ContainerID), []string{"/bin/sh", "-l"}, plan.Home, cols, rows)
		if err != nil {
			return fmt.Errorf("scheduler: exec terminal tab %q fallback: %w", tab, err)
		}
	}
	if err := s.cfg.PTY.StartSession(ctx, ptyhost.TerminalSession(member, tab), att); err != nil {
		_ = att.Close()
		return fmt.Errorf("scheduler: start terminal tab %q: %w", tab, err)
	}
	return nil
}

// TerminalStatus returns the current terminal and tab state for a member.
func (s *Scheduler) TerminalStatus(ctx context.Context, member domain.MemberID) (domain.TerminalStatus, error) {
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return domain.TerminalStatus{}, fmt.Errorf("scheduler: get member for terminal status: %w", err)
	}
	row, err := s.cfg.Store.GetTerminal(ctx, member)
	if errors.Is(err, store.ErrNotFound) {
		return domain.TerminalStatus{SavedImage: m.Image}, nil
	}
	if err != nil {
		return domain.TerminalStatus{}, fmt.Errorf("scheduler: get terminal status: %w", err)
	}
	tabs := terminalTabs(s.cfg.PTY.ActiveSessions(terminalPrefix(member)), member)
	sup, cleanupPending := s.terminalSnapshot(member)
	running := !cleanupPending && (len(tabs) > 0 || sup != nil)
	return domain.TerminalStatus{Running: running, Image: row.Image, SavedImage: m.Image, StartedAt: row.StartedAt, Tabs: tabs}, nil
}

func terminalTabs(keys []ptyhost.SessionKey, member domain.MemberID) []string {
	prefix := terminalPrefix(member)
	tabs := make([]string, 0, len(keys))
	for _, key := range keys {
		name := strings.TrimPrefix(string(key), prefix)
		if name != string(key) {
			tabs = append(tabs, name)
		}
	}
	slices.Sort(tabs)
	return tabs
}

// StopTerminal stops a member's terminal and all of its tab sessions.
func (s *Scheduler) StopTerminal(ctx context.Context, member domain.MemberID) error {
	return s.WithStoppedTerminal(ctx, member, nil)
}

// WithStoppedTerminal keeps EnsureTerminal excluded through an operation that
// follows confirmed stop, such as deleting the member row and home. A non-nil
// afterStop is member removal, so the kept container is destroyed first.
// afterStop may acquire the member cache lock (terminal -> cache ordering), but
// must not reenter terminal lifecycle methods. Nil performs only the stop.
func (s *Scheduler) WithStoppedTerminal(ctx context.Context, member domain.MemberID, afterStop func() error) error {
	// Interrupt OAuth completion before waiting on its short terminal-locked
	// native setup. stopTerminalLocked repeats this under the admission fence.
	s.stopTerminalGitHubOAuth(member)
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()
	if err := s.stopTerminalLocked(ctx, member); err != nil {
		return err
	}
	s.touchTerminalCache(member)
	if afterStop != nil {
		if _, err := s.destroyKeptTerminalLocked(ctx, member, false); err != nil {
			return err
		}
		return afterStop()
	}
	return nil
}

// stopTerminalLocked stops a member's terminal while its member lock is held.
// The stopped container is kept; ResetEnvironment and member removal destroy it.
func (s *Scheduler) stopTerminalLocked(ctx context.Context, member domain.MemberID) error {
	s.stopTerminalGitHubOAuth(member)
	row, err := s.cfg.Store.GetTerminal(ctx, member)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("scheduler: get terminal to stop: %w", err)
	}
	var sup *terminalSupervision
	s.mu.Lock()
	sup = s.terminals[member]
	s.mu.Unlock()
	cid := ""
	if sup != nil {
		cid = string(sup.containerID)
	} else if row != nil {
		cid = row.ContainerID
	}
	if cid == "" {
		if sc, sidecarErr := s.readTerminalSidecar(member); sidecarErr == nil {
			cid = sc.ContainerID
		}
	}
	s.cfg.PTY.StopSessionsWithPrefix(ctx, terminalPrefix(member))
	if cid != "" {
		if stopErr := s.cfg.Runtime.Stop(ctx, runtime.ID(cid), s.cfg.StopGrace); stopErr != nil && !errors.Is(stopErr, runtime.ErrNotFound) {
			return fmt.Errorf("scheduler: stop terminal: %w", stopErr)
		}
	}
	if err := s.cfg.Store.DeleteTerminal(ctx, member); err != nil {
		return fmt.Errorf("scheduler: delete terminal record: %w", err)
	}
	s.releaseTerminalCoordination(member)
	s.mu.Lock()
	if current := s.terminals[member]; current == sup {
		delete(s.terminals, member)
	}
	s.mu.Unlock()
	s.releaseTerminalReservation(sup)
	return nil
}

func (s *Scheduler) recoverTerminals(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: list terminal members: %w", err)
	}
	for _, member := range members {
		if ctx.Err() != nil {
			return nil
		}
		lock := s.terminalLock(member.ID)
		lock.Lock()
		if s.lookupTerminal(member.ID) != nil {
			lock.Unlock()
			continue
		}
		row, err := s.cfg.Store.GetTerminal(ctx, member.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			lock.Unlock()
			return fmt.Errorf("scheduler: recover terminal %q: %w", member.ID, err)
		}
		if adoptErr := s.recoverTerminalLocked(ctx, member, row); adoptErr != nil {
			if s.lookupTerminal(member.ID) == nil {
				lock.Unlock()
				return fmt.Errorf("scheduler: recover terminal %q: %w", member.ID, adoptErr)
			}
			slog.Warn("scheduler: recover terminal", "member", member.ID, "error", adoptErr)
		}
		lock.Unlock()
	}
	return nil
}

// recoverTerminalLocked re-adopts one persisted terminal on startup: the
// stored container when it still runs, else a creation-key match (the row
// went stale), else the row is pruned so the next open recreates. When no row
// exists, a creation-key survivor is adopted but no replacement is created.
// Durable cleanup is conditional on the row still naming this container, so a
// replacement cannot be removed by an old recovery attempt.
func (s *Scheduler) recoverTerminalLocked(ctx context.Context, member *domain.Member, row *domain.Terminal) error {
	if row == nil {
		found, findErr := s.cfg.Runtime.FindByCreationKey(ctx, terminalCreationKey(member.ID))
		switch {
		case findErr == nil:
			adopted, adoptedOK, err := s.tryAdoptTerminal(ctx, member, nil, found)
			if err != nil {
				return err
			}
			if !adoptedOK {
				return nil
			}
			_, err = s.finishTerminalAdoption(ctx, adopted)
			return err
		case errors.Is(findErr, runtime.ErrNotFound):
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			return fmt.Errorf("scheduler: find terminal container: %w", findErr)
		}
	}

	adopted, adoptedOK, err := s.tryAdoptTerminal(ctx, member, row, runtime.ID(row.ContainerID))
	if err != nil {
		return err
	}
	if !adoptedOK {
		found, findErr := s.cfg.Runtime.FindByCreationKey(ctx, terminalCreationKey(member.ID))
		switch {
		case findErr == nil && string(found) != row.ContainerID:
			if err = s.destroyTerminalContainer(ctx, runtime.ID(row.ContainerID)); err != nil {
				return fmt.Errorf("scheduler: destroy stale terminal: %w", err)
			}
			adopted, adoptedOK, err = s.tryAdoptTerminal(ctx, member, row, found)
			if err != nil {
				return err
			}
		case findErr == nil:
			// The creation-key match is the same exited container.
		case errors.Is(findErr, runtime.ErrNotFound):
			// No survivor exists; prune the stale row below.
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			return fmt.Errorf("scheduler: find terminal replacement: %w", findErr)
		}
	}
	if !adoptedOK {
		return s.deleteTerminalRecordIfMatches(ctx, member.ID, row.ContainerID)
	}
	_, err = s.finishTerminalAdoption(ctx, adopted)
	return err
}

func (s *Scheduler) deleteTerminalRecordIfMatches(ctx context.Context, member domain.MemberID, containerID string) error {
	current, err := s.cfg.Store.GetTerminal(ctx, member)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scheduler: get terminal record for cleanup: %w", err)
	}
	if current.ContainerID != containerID {
		return nil
	}
	if err := s.cfg.Store.DeleteTerminal(ctx, member); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("scheduler: delete terminal record: %w", err)
	}
	return nil
}
