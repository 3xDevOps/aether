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
)

var (
	ErrInvalidTerminalTab = errors.New("terminal: invalid tab")
	ErrTerminalTabLimit   = errors.New("terminal: at most 6 tabs")
	terminalTabPattern    = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

type terminalSupervision struct {
	member          domain.MemberID
	containerID     runtime.ID
	image           string
	startedAt       time.Time
	home            string
	runUser         string
	userReservation *credentialUserReservation
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
	s.mu.Lock()
	entry := s.terminals[member]
	if entry == nil {
		s.mu.Unlock()
		return "", errors.New("environment terminal is not running")
	}
	containerID := entry.containerID
	s.mu.Unlock()
	return s.cfg.Runtime.ContainerIP(ctx, containerID)
}

// EnsureTerminal creates or adopts one long-lived terminal container for a member.
func (s *Scheduler) EnsureTerminal(ctx context.Context, member domain.MemberID) (*domain.Terminal, error) {
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()
	return s.ensureTerminalLocked(ctx, member)
}

func (s *Scheduler) ensureTerminalLocked(ctx context.Context, member domain.MemberID) (*domain.Terminal, error) {
	if existing := s.lookupTerminal(member); existing != nil {
		return &domain.Terminal{Member: member, ContainerID: string(existing.containerID), Image: existing.image, StartedAt: existing.startedAt}, nil
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
		if adoptErr != nil {
			return nil, adoptErr
		}
		if adoptedOK {
			terminal := adopted.terminal
			if row == nil || row.ContainerID != terminal.ContainerID {
				if putErr := s.cfg.Store.PutTerminal(ctx, terminal); putErr != nil {
					return nil, fmt.Errorf("scheduler: persist terminal: %w", putErr)
				}
			}
			s.registerAdoptedTerminal(adopted)
			return terminal, nil
		}
	} else if !errors.Is(findErr, runtime.ErrNotFound) {
		return nil, fmt.Errorf("scheduler: find terminal container: %w", findErr)
	}
	if row != nil && (findErr != nil || row.ContainerID != string(cid)) {
		adopted, adoptedOK, adoptErr := s.tryAdoptTerminal(ctx, m, row, runtime.ID(row.ContainerID))
		if adoptErr != nil {
			return nil, adoptErr
		}
		if adoptedOK {
			s.registerAdoptedTerminal(adopted)
			return adopted.terminal, nil
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
		Name:        terminalContainerName(member),
		Image:       plan.Image,
		Env:         plan.Env,
		WorkingDir:  plan.Home,
		Command:     []string{"/bin/bash", "-l"},
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
	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err != nil {
		s.destroyTerminalAfterFailure(terminalReservation, cid)
		return nil, fmt.Errorf("scheduler: attach terminal: %w", err)
	}
	if sessionErr := s.cfg.PTY.StartSession(ctx, ptyhost.TerminalSession(member, terminalTabMain), att); sessionErr != nil {
		_ = att.Close()
		s.destroyTerminalAfterFailure(terminalReservation, cid)
		return nil, fmt.Errorf("scheduler: start terminal session: %w", sessionErr)
	}
	terminal := &domain.Terminal{Member: member, ContainerID: string(cid), Image: plan.Image, StartedAt: startedAt}
	if putErr := s.cfg.Store.PutTerminal(ctx, terminal); putErr != nil {
		s.cfg.PTY.StopSessionsWithPrefix(context.Background(), terminalPrefix(member))
		s.destroyTerminalAfterFailure(terminalReservation, cid)
		return nil, fmt.Errorf("scheduler: persist terminal: %w", putErr)
	}
	s.registerTerminal(terminal, terminalReservation.userReservation, plan.Home)
	return terminal, nil
}

// createAndStartTerminal creates and starts the terminal container,
// retrying once with /bin/sh -l when the image has no bash. Docker only
// reports the missing shell at start (exec stat happens in runc), so the
// probe is the start error text and the retry needs a fresh container.
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
	if !isMissingShell(startErr, spec.Command[0]) {
		return "", fmt.Errorf("scheduler: start terminal: %w", startErr)
	}
	spec.Command = []string{"/bin/sh", "-l"}
	cid, err = s.cfg.Runtime.Create(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("scheduler: create terminal (sh fallback): %w", err)
	}
	if err := s.cfg.Runtime.Start(ctx, cid); err != nil {
		_ = s.cfg.Runtime.Destroy(context.Background(), cid)
		return "", fmt.Errorf("scheduler: start terminal (sh fallback): %w", err)
	}
	return cid, nil
}

// isMissingShell recognizes runc's missing-executable start failure for
// the given shell path.
func isMissingShell(err error, shell string) bool {
	msg := err.Error()
	return strings.Contains(msg, shell) &&
		(strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "executable file not found"))
}

func (s *Scheduler) lookupTerminal(member domain.MemberID) *terminalSupervision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminals[member]
}

func (s *Scheduler) registerAdoptedTerminal(adopted *terminalAdoption) {
	s.registerTerminal(adopted.terminal, adopted.reservation, adopted.home)
}

func (s *Scheduler) registerTerminal(terminal *domain.Terminal, reservation *credentialUserReservation, home string) {
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
		return
	}
	if reservation != nil {
		reservation.terminal = sup
		reservation.pending = false
	}
	s.terminals[terminal.Member] = sup
	s.mu.Unlock()
	s.wg.Add(1)
	go s.superviseTerminal(sup)
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
	terminal    *domain.Terminal
	home        string
	reservation *credentialUserReservation
}

func (s *Scheduler) destroyTerminalAfterFailure(entry *terminalSupervision, cid runtime.ID) {
	if err := s.cfg.Runtime.Destroy(context.Background(), cid); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		slog.Warn("scheduler: destroy failed terminal after provisioning error", "member", entry.member, "container", cid, "error", err)
		return
	}
	s.releaseTerminalReservation(entry)
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
		_ = s.cfg.Runtime.Destroy(context.Background(), cid)
		return nil, false, nil
	case errors.Is(waitErr, runtime.ErrNotFound):
		return nil, false, nil
	case errors.Is(waitErr, context.DeadlineExceeded):
		// Still running: adopt it.
	case ctx.Err() != nil:
		return nil, false, ctx.Err()
	default:
		return nil, false, fmt.Errorf("scheduler: probe terminal container: %w", waitErr)
	}
	info, err := s.cfg.Runtime.Inspect(ctx, cid)
	if err != nil {
		return nil, false, fmt.Errorf("scheduler: inspect terminal container: %w", err)
	}
	user, home, err := terminalContainerMetadata(info)
	if err != nil {
		return nil, false, err
	}
	reservationEntry := &terminalSupervision{member: member.ID, home: home}
	if reserveErr := s.reserveTerminalUser(reservationEntry, user); reserveErr != nil {
		return nil, false, reserveErr
	}
	terminal, err := s.attachTerminalLocked(ctx, member, row, cid, info.Image)
	if err != nil {
		s.releaseTerminalReservation(reservationEntry)
		return nil, false, fmt.Errorf("scheduler: attach terminal: %w", err)
	}
	return &terminalAdoption{
		terminal:    terminal,
		home:        home,
		reservation: reservationEntry.userReservation,
	}, true, nil
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

func (s *Scheduler) attachTerminalLocked(ctx context.Context, member *domain.Member, row *domain.Terminal, cid runtime.ID, image string) (*domain.Terminal, error) {
	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err != nil {
		return nil, err
	}
	if !s.hasTerminalSession(member.ID, terminalTabMain) {
		if sessionErr := s.cfg.PTY.StartSession(ctx, ptyhost.TerminalSession(member.ID, terminalTabMain), att); sessionErr != nil {
			_ = att.Close()
			return nil, fmt.Errorf("scheduler: start terminal session: %w", sessionErr)
		}
	} else {
		_ = att.Close()
	}
	terminal := &domain.Terminal{Member: member.ID, ContainerID: string(cid), Image: image, StartedAt: time.Now().UTC()}
	if row != nil {
		if terminal.Image == "" {
			terminal.Image = row.Image
		}
		terminal.StartedAt = row.StartedAt
	}
	if terminal.Image == "" {
		terminal.Image = member.Image
	}
	if terminal.Image == "" {
		terminal.Image = s.cfg.StandardImage
	}
	return terminal, nil
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

func (s *Scheduler) superviseTerminal(sup *terminalSupervision) {
	defer s.wg.Done()
	_, _ = s.cfg.Runtime.Wait(s.superCtx, sup.containerID)
	if s.superCtx.Err() != nil {
		return
	}
	// The main process exited. Clean up under the member lock so a
	// concurrent Ensure or Stop never observes a half-cleaned terminal,
	// and only when this supervision still owns the member's entry: a
	// StopTerminal that raced the exit has already cleaned up, and its
	// successor terminal must not lose its row or sessions to us.
	lock := s.terminalLock(sup.member)
	lock.Lock()
	defer lock.Unlock()
	s.mu.Lock()
	owned := s.terminals[sup.member] == sup
	if owned {
		delete(s.terminals, sup.member)
	}
	s.mu.Unlock()
	if !owned {
		return
	}
	s.cfg.PTY.StopSessionsWithPrefix(context.Background(), terminalPrefix(sup.member))
	_ = s.cfg.Runtime.Destroy(context.Background(), sup.containerID)
	_ = s.cfg.Store.DeleteTerminal(context.Background(), sup.member)
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
	running := len(tabs) > 0 || s.lookupTerminal(member) != nil
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
		row, err := s.cfg.Store.GetTerminal(ctx, member.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("scheduler: recover terminal %q: %w", member.ID, err)
		}
		lock := s.terminalLock(member.ID)
		lock.Lock()
		if s.lookupTerminal(member.ID) == nil {
			if adoptErr := s.recoverTerminalLocked(ctx, member, row); adoptErr != nil {
				slog.Warn("scheduler: recover terminal", "member", member.ID, "error", adoptErr)
			}
		}
		lock.Unlock()
	}
	return nil
}

// recoverTerminalLocked re-adopts one persisted terminal on startup: the
// stored container when it still runs, else a creation-key match (the row
// went stale), else the row is pruned so the next open recreates.
func (s *Scheduler) recoverTerminalLocked(ctx context.Context, member *domain.Member, row *domain.Terminal) error {
	adopted, adoptedOK, err := s.tryAdoptTerminal(ctx, member, row, runtime.ID(row.ContainerID))
	if err != nil {
		return err
	}
	if !adoptedOK {
		if found, findErr := s.cfg.Runtime.FindByCreationKey(ctx, terminalCreationKey(member.ID)); findErr == nil && string(found) != row.ContainerID {
			adopted, adoptedOK, err = s.tryAdoptTerminal(ctx, member, row, found)
			if err != nil {
				return err
			}
		}
	}
	if !adoptedOK {
		return s.cfg.Store.DeleteTerminal(ctx, member.ID)
	}
	if adopted.terminal.ContainerID != row.ContainerID {
		if putErr := s.cfg.Store.PutTerminal(ctx, adopted.terminal); putErr != nil {
			return fmt.Errorf("scheduler: persist recovered terminal: %w", putErr)
		}
	}
	s.registerAdoptedTerminal(adopted)
	return nil
}
