package coord

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const enhancedWakeTimeout = 20 * time.Second

// wakeSweepInterval bounds how long an idle Enhanced session waits for a
// wake it is owed when no event retries it.
const wakeSweepInterval = 30 * time.Second

// enhancedWakeRetries spaces the retries of a wake that admission refused,
// such as for busy run control, which announces no release.
var enhancedWakeRetries = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// SessionWaker prompts the agent sessions the server drives itself.
type SessionWaker interface {
	// Wakeable reports whether run's session takes a prompt now: an
	// enhanced session with no turn running and no prompt queued, or a
	// Standard Codex terminal attached to its app-server, which takes one
	// mid-turn as a steer.
	Wakeable(run domain.RunID) bool
	// Wake hands the session prompt, and fails unless it is still
	// wakeable.
	Wake(ctx context.Context, run domain.RunID, prompt string) error
	// InTurn reports a wakeable session in a turn, which takes a prompt as
	// a steer.
	InTurn(run domain.RunID) bool
}

// WakeIdle retries the run's owed wake: its hook observers rerun
// admission and a session the server drives is offered the prompt.
func (s *Service) WakeIdle(run domain.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offerWakeLocked(run)
}

func (s *Service) offerWakeLocked(run domain.RunID) {
	for waiter := range s.hookWaiters[run] {
		waiter.consumer = waiter.consumer || s.inboxConsumers[run] > 0
		waiter.signal()
	}
	s.wakeEnhancedLocked(run)
}

func (s *Service) sweepEnhancedWakes() {
	if s.cfg.Waker == nil || s.cfg.WakeAdmission == nil {
		return
	}
	ticker := time.NewTicker(s.cfg.wakeSweep)
	defer ticker.Stop()
	for {
		select {
		case <-s.serveCtx.Done():
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		for key := range s.listeners {
			run := key.run
			if _, pending := s.enhancedSweeps[run]; pending {
				continue
			}
			s.enhancedSweeps[run] = struct{}{}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.wakeEnhanced(run)
				s.mu.Lock()
				delete(s.enhancedSweeps, run)
				s.mu.Unlock()
			}()
		}
		s.mu.Unlock()
	}
}

// An agent waiting in aether-internal inbox takes new mail from that call.
func (s *Service) wakeEnhancedLocked(run domain.RunID) {
	if s.closed || s.cfg.Disabled || s.cfg.Waker == nil || s.cfg.WakeAdmission == nil || s.inboxConsumers[run] > 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for _, delay := range enhancedWakeRetries {
			if !s.wakeEnhanced(run) {
				return
			}
			select {
			case <-s.serveCtx.Done():
				return
			case <-time.After(delay):
			}
		}
		s.wakeEnhanced(run)
	}()
}

// Each set of unread message IDs wakes at most once. retry means admission refused the wake while the session stayed
// idle, which no turn end will retry.
func (s *Service) wakeEnhanced(run domain.RunID) (retry bool) {
	mail, ok := s.cfg.Mail.(store.UnackedRunMessageIDsStore)
	if !ok || !s.cfg.Waker.Wakeable(run) || !s.enterRun(run) {
		return false
	}
	defer s.leaveRun(run)
	lock := s.enhancedWakeLock(run)
	lock.Lock()
	defer lock.Unlock()
	s.mu.Lock()
	opens := s.enhancedOpens[run]
	if refusal, ok := s.enhancedRefusals[run]; ok && refusal.opens != opens {
		delete(s.enhancedWoken, run)
		delete(s.enhancedRefusals, run)
	}
	s.mu.Unlock()
	ctx, cancel := s.wakeContext(s.serveCtx, run, enhancedWakeTimeout)
	defer cancel()
	ids, err := mail.ListUnackedRunMessageIDs(ctx, run, protocol.CoordMaxUnread)
	if err != nil {
		slog.Warn("coord: enhanced wake: list unread mail", "run", run, "error", err)
		return false
	}
	freshMail := s.unwoken(run, ids)
	// Like the hooks, tell only a working agent about overlapping edits, once
	// per set of them.
	var overlap string
	inTurn := s.cfg.Waker.InTurn(run)
	if inTurn {
		overlap = s.overlapState(ctx, run)
	}
	s.mu.Lock()
	if inTurn && overlap == "" {
		delete(s.enhancedOverlaps, run)
	}
	freshOverlap := overlap != "" && overlap != s.enhancedOverlaps[run]
	s.mu.Unlock()
	if !freshMail && !freshOverlap {
		return false
	}
	overlapSent := false
	err = s.cfg.WakeAdmission(ctx, run, func() error {
		// An agent waiting in aether-internal inbox takes the mail from that
		// call; the admission lock orders this against the wait's start.
		admission := s.inboxAdmissionLock(run)
		admission.Lock()
		defer admission.Unlock()
		s.mu.Lock()
		reading := s.inboxConsumers[run] > 0
		s.mu.Unlock()
		var prompt strings.Builder
		if len(ids) > 0 && !reading {
			prompt.WriteString(protocol.CoordInboxContext(len(ids)))
		}
		// The turn may have ended while admission waited.
		steers := s.cfg.Waker.InTurn(run)
		overlapSent = freshOverlap && steers
		if overlapSent {
			prompt.WriteString(protocol.CoordOverlapContext)
		}
		if prompt.Len() == 0 {
			return nil
		}
		if !steers {
			prompt.WriteString(protocol.CoordReportReminder)
		}
		// Admission may have spent ctx waiting for a lock; a turn that starts
		// but times out unconfirmed goes unrecorded, and its mail wakes again.
		promptCtx, cancel := s.wakeContext(s.serveCtx, run, enhancedWakeTimeout)
		defer cancel()
		return s.cfg.Waker.Wake(promptCtx, run, prompt.String())
	})
	// An agent that refused the prompt still had it, and would refuse the
	// same wake again at once.
	if err != nil && !errors.Is(err, domain.ErrPromptRefused) {
		slog.Debug("coord: enhanced wake suppressed", "run", run, "error", err)
		return s.cfg.Waker.Wakeable(run)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, refused := s.enhancedRefusals[run]; err != nil && !refused {
		s.enhancedRefusals[run] = wakeRefusal{opens: opens}
	}
	woken := s.enhancedWoken[run]
	if woken == nil {
		woken = make(map[string]struct{}, len(ids))
		s.enhancedWoken[run] = woken
	}
	for _, id := range ids {
		woken[id] = struct{}{}
	}
	if overlapSent {
		s.enhancedOverlaps[run] = overlap
	}
	return false
}

// overlapState names the peers and files the run overlaps, the state the
// hooks announce once per change.
func (s *Service) overlapState(ctx context.Context, run domain.RunID) string {
	active, err := s.radar.authorizedSet(ctx, run)
	if err != nil {
		slog.Debug("coord: enhanced wake: read overlaps", "run", run, "error", err)
		return ""
	}
	var overlap []string
	for _, p := range active {
		for _, file := range p.files {
			overlap = append(overlap, string(p.run)+":"+file)
		}
	}
	slices.Sort(overlap)
	return strings.Join(overlap, "\n")
}

// wakeRefusal is a wake the agent refused. It counts as delivered only to
// the opens sessions opened before it; a later session is woken for its mail.
type wakeRefusal struct {
	opens uint64
}

func (s *Service) EnhancedSessionOpened(run domain.RunID) {
	if s.cfg.Disabled || !s.enterRun(run) {
		return
	}
	defer s.leaveRun(run)
	s.mu.Lock()
	s.enhancedOpens[run]++
	s.mu.Unlock()
}

func (s *Service) unwoken(run domain.RunID, ids []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	woken := s.enhancedWoken[run]
	current := make(map[string]struct{}, len(ids))
	fresh := false
	for _, id := range ids {
		current[id] = struct{}{}
		if _, ok := woken[id]; !ok {
			fresh = true
		}
	}
	for id := range woken {
		if _, ok := current[id]; !ok {
			delete(woken, id)
		}
	}
	if len(woken) == 0 {
		delete(s.enhancedWoken, run)
	}
	return fresh
}

func (s *Service) enhancedWakeLock(run domain.RunID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.enhancedWakeLocks[run]
	if lock == nil {
		lock = &sync.Mutex{}
		s.enhancedWakeLocks[run] = lock
	}
	return lock
}

// RemindReport hands an idle session the server drives prompt, the reminder
// to report the turn that just ended, under ReminderAdmission: a protected
// run, a child run a human holds or a run a member controls is not prompted.
func (s *Service) RemindReport(ctx context.Context, run domain.RunID, prompt string) error {
	if s.cfg.Disabled || s.cfg.Waker == nil || s.cfg.ReminderAdmission == nil {
		return errors.New("coord: report reminder: no session waker")
	}
	if !s.enterRun(run) {
		return errors.New("coord: report reminder: the run is closing")
	}
	defer s.leaveRun(run)
	return s.cfg.ReminderAdmission(ctx, run, func() error { return s.cfg.Waker.Wake(ctx, run, prompt) })
}
