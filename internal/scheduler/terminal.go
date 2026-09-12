package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
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
	return s.ensureTerminalLocked(ctx, member)
}

func (s *Scheduler) ensureTerminalLocked(ctx context.Context, member domain.MemberID) (*domain.Terminal, error) {
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
	}

	plan, err := s.BuildEnvironmentPlan(ctx, nil, nil, m, harness.Profile{}, EnvironmentPurposeTerminal)
	if err != nil {
		return nil, fmt.Errorf("scheduler: build terminal environment: %w", err)
	}
	terminalReservation := &terminalSupervision{member: member}
	if reserveErr := s.reserveTerminalUser(terminalReservation, plan.User); reserveErr != nil {
		return nil, fmt.Errorf("scheduler: reserve terminal user: %w", reserveErr)
	}
	if ownershipErr := s.applyRunOwnership(nil, &domain.Run{}, plan.Mounts, plan.User); ownershipErr != nil {
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
		Command:     []string{"/bin/sh", "-c", "if [ -x /bin/bash ]; then exec /bin/bash -l; else exec /bin/sh -l; fi"},
		TTY:         true,
		Mounts:      plan.Mounts,
		User:        plan.User,
		CreationKey: key,
	}
	cid, err = s.createAndStartTerminal(ctx, spec)
	if err != nil {
		s.releaseTerminalReservation(terminalReservation)
		return nil, err
	}
	terminalReservation.containerID = cid
	terminal := &domain.Terminal{Member: member, ContainerID: string(cid), Image: plan.Image, StartedAt: startedAt}
	sup := s.registerTerminal(terminal, terminalReservation.userReservation, plan.Home)
	sup.persistPending = true
	if err := s.ensureTerminalReady(ctx, sup, terminal); err != nil {
		return nil, err
	}
	return terminal, nil
}

func (s *Scheduler) createAndStartTerminal(ctx context.Context, spec runtime.Spec) (runtime.ID, error) {
	cid, err := s.cfg.Runtime.Create(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("scheduler: create terminal: %w", err)
	}
	startErr := s.cfg.Runtime.Start(ctx, cid)
	if startErr == nil {
		return cid, nil
	}
	_ = s.cfg.Runtime.Destroy(context.Background(), cid)
	return "", fmt.Errorf("scheduler: start terminal: %w", startErr)
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
		if other == reservation || other.memberID != sup.member {
			continue
		}
		if other.user != user {
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
		if other == sup.userReservation || other.memberID != sup.member {
			continue
		}
		if other.user != sup.runUser {
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
		memberID: entry.member,
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
// exited container is destroyed so the caller falls through to create a
// fresh one: the plan's contract is that exiting the shell and reopening
// recreates the environment. The probe mirrors recoverSupervised: a short
// non-destructive Wait whose deadline means "still running".
func (s *Scheduler) tryAdoptTerminal(ctx context.Context, member *domain.Member, row *domain.Terminal, cid runtime.ID) (*terminalAdoption, bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, exitProbeTimeout)
	_, waitErr := s.cfg.Runtime.Wait(probeCtx, cid)
	cancel()
	switch {
	case waitErr == nil:
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), terminalCleanupTimeout)
		defer cleanupCancel()
		if destroyErr := s.cfg.Runtime.Destroy(cleanupCtx, cid); destroyErr != nil && !errors.Is(destroyErr, runtime.ErrNotFound) {
			s.retainTerminalAfterRuntimeError(member, row, cid)
			return nil, false, fmt.Errorf("scheduler: destroy exited terminal: %w", destroyErr)
		}
		return nil, false, nil
	case errors.Is(waitErr, runtime.ErrNotFound):
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
	for _, active := range s.cfg.PTY.ActiveSessions(terminalPrefix(member)) {
		if active == key {
			return true
		}
	}
	return false
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

// cleanupExitedTerminalLocked removes an exited terminal only after both its
// container and its matching durable row are handled. The supervision entry
// stays registered while either operation can be retried, so a later Ensure
// cannot create a replacement that an old supervisor would delete.
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

	cleanupCtx, cancel := context.WithTimeout(ctx, terminalCleanupTimeout)
	defer cancel()
	s.cfg.PTY.StopSessionsWithPrefix(cleanupCtx, terminalPrefix(sup.member))
	if err := s.cfg.Runtime.Destroy(cleanupCtx, sup.containerID); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("scheduler: destroy exited terminal: %w", err)
	}

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
	terminal, err := s.ensureTerminalLocked(ctx, member)
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
	plan, err := s.BuildEnvironmentPlan(ctx, nil, nil, memberRow, harness.Profile{}, EnvironmentPurposeTerminal)
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
	sort.Strings(tabs)
	return tabs
}

// StopTerminal stops a member's terminal and all of its tab sessions.
func (s *Scheduler) StopTerminal(ctx context.Context, member domain.MemberID) error {
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()
	return s.stopTerminalLocked(ctx, member)
}

// stopTerminalLocked stops a member's terminal while its member lock is held.
func (s *Scheduler) stopTerminalLocked(ctx context.Context, member domain.MemberID) error {
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
	s.cfg.PTY.StopSessionsWithPrefix(ctx, terminalPrefix(member))
	if cid != "" {
		if stopErr := s.cfg.Runtime.Stop(ctx, runtime.ID(cid), s.cfg.StopGrace); stopErr != nil && !errors.Is(stopErr, runtime.ErrNotFound) {
			return fmt.Errorf("scheduler: stop terminal: %w", stopErr)
		}
		if destroyErr := s.cfg.Runtime.Destroy(ctx, runtime.ID(cid)); destroyErr != nil && !errors.Is(destroyErr, runtime.ErrNotFound) {
			return fmt.Errorf("scheduler: destroy terminal: %w", destroyErr)
		}
	}
	if err := s.cfg.Store.DeleteTerminal(ctx, member); err != nil {
		return fmt.Errorf("scheduler: delete terminal record: %w", err)
	}
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
		if ctx.Err() != nil {
			return nil
		}
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
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("scheduler: recover terminal %q: %w", member.ID, err)
		}
		if adoptErr := s.recoverTerminalLocked(ctx, member, row); adoptErr != nil {
			if s.lookupTerminal(member.ID) == nil {
				lock.Unlock()
				if ctx.Err() != nil {
					return nil
				}
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
