package coord

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const enhancedWakeTimeout = 20 * time.Second

// enhancedWakeRetries spaces the retries of a wake that run control or a
// mission lock refused, neither of which announces its release.
var enhancedWakeRetries = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// ACPWaker starts turns in enhanced runs, whose agents load no hook that
// could notice new mail on their own.
type ACPWaker interface {
	// IdleEnhanced reports whether run is an enhanced run whose agent
	// session is open with no turn running and no prompt queued.
	IdleEnhanced(run domain.RunID) bool
	// WakeEnhanced starts one turn with prompt, and fails unless the
	// session is still idle.
	WakeEnhanced(ctx context.Context, run domain.RunID, prompt string) error
}

// WakeIdle wakes an enhanced run that just became idle if it holds mail no
// earlier wake announced.
func (s *Service) WakeIdle(run domain.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wakeEnhancedLocked(run)
}

func (s *Service) wakeEnhancedLocked(run domain.RunID) {
	if s.closed || s.cfg.Disabled || s.cfg.ACPWaker == nil || s.cfg.WakeAdmission == nil {
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

// wakeEnhanced prompts an idle enhanced run once per set of unread message
// IDs: a turn the agent ends without reading its mail is not repeated until
// another message arrives. It reports whether admission refused the wake
// while the session stayed idle, which no turn end will retry.
func (s *Service) wakeEnhanced(run domain.RunID) (retry bool) {
	mail, ok := s.cfg.Mail.(store.UnackedRunMessageIDsStore)
	if !ok || !s.cfg.ACPWaker.IdleEnhanced(run) || !s.enterRun(run) {
		return false
	}
	defer s.leaveRun(run)
	lock := s.enhancedWakeLock(run)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(s.serveCtx, enhancedWakeTimeout)
	defer cancel()
	ids, err := mail.ListUnackedRunMessageIDs(ctx, run, protocol.CoordMaxUnread)
	if err != nil {
		slog.Warn("coord: enhanced wake: list unread mail", "run", run, "error", err)
		return false
	}
	if !s.unwoken(run, ids) {
		return false
	}
	err = s.cfg.WakeAdmission(ctx, run, func() error {
		return s.cfg.ACPWaker.WakeEnhanced(ctx, run, protocol.CoordInboxContext(len(ids)))
	})
	if err != nil {
		slog.Debug("coord: enhanced wake suppressed", "run", run, "error", err)
		return s.cfg.ACPWaker.IdleEnhanced(run)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	woken := s.enhancedWoken[run]
	if woken == nil {
		woken = make(map[string]struct{}, len(ids))
		s.enhancedWoken[run] = woken
	}
	for _, id := range ids {
		woken[id] = struct{}{}
	}
	return false
}

// unwoken forgets woken IDs that are no longer unread and reports whether
// any unread ID is still unannounced.
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
