package coord

import (
	"cmp"
	"context"
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

// enhancedWakeRetries spaces the retries of a wake that run control or a
// mission lock refused, neither of which announces its release.
var enhancedWakeRetries = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

type ACPWaker interface {
	// IdleEnhanced reports whether run is an enhanced run whose agent
	// session is open with no turn running and no prompt queued.
	IdleEnhanced(run domain.RunID) bool
	// WakeEnhanced starts one turn with prompt, and fails unless the
	// session is still idle.
	WakeEnhanced(ctx context.Context, run domain.RunID, prompt string) error
}

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

// Each set of unread message IDs and each integrator notice wakes at most
// once. retry means admission refused the wake while the session stayed
// idle, which no turn end will retry.
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
	notice := s.integratorNotice(ctx, run)
	freshMail := s.unwoken(run, ids)
	changes := s.missionChanges(run, notice)
	if !freshMail && len(changes) == 0 {
		return false
	}
	var prompt strings.Builder
	if len(ids) > 0 {
		prompt.WriteString(protocol.CoordInboxContext(len(ids)))
	}
	if len(changes) > 0 {
		prompt.WriteString(protocol.CoordMissionUpdateContext(notice.mission, changes))
	}
	err = s.cfg.WakeAdmission(ctx, run, func() error {
		return s.cfg.ACPWaker.WakeEnhanced(ctx, run, prompt.String())
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
	if len(changes) > 0 {
		s.enhancedNotices[run] = notice
	}
	return false
}

type missionNotice struct {
	mission string
	seq     uint64
	changes map[domain.MissionChange]uint64
}

func (s *Service) integratorNotice(ctx context.Context, run domain.RunID) missionNotice {
	if s.cfg.Mission == nil {
		return missionNotice{}
	}
	a, err := s.cfg.Mission.Assignment(ctx, run)
	if err != nil {
		slog.Debug("coord: enhanced wake: read mission assignment", "run", run, "error", err)
		return missionNotice{}
	}
	if a.Role != "integrator" {
		return missionNotice{}
	}
	return missionNotice{mission: a.MissionID, seq: a.ChangeSeq, changes: a.Changes}
}

// The first notice seen is the baseline: the integrator's own task already
// describes the mission it starts in.
func (s *Service) missionChanges(run domain.RunID, notice missionNotice) []domain.MissionChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	if notice.mission == "" {
		delete(s.enhancedNotices, run)
		return nil
	}
	seen, ok := s.enhancedNotices[run]
	if !ok {
		s.enhancedNotices[run] = notice
		return nil
	}
	var changes []domain.MissionChange
	for kind, seq := range notice.changes {
		if seq > seen.seq {
			changes = append(changes, kind)
		}
	}
	if len(changes) == 0 {
		s.enhancedNotices[run] = notice
		return nil
	}
	slices.SortFunc(changes, func(a, b domain.MissionChange) int { return cmp.Compare(notice.changes[a], notice.changes[b]) })
	return changes
}

// EnhancedSessionOpened takes the baseline before the session's first turn,
// so a change during that turn wakes it when the turn ends.
func (s *Service) EnhancedSessionOpened(ctx context.Context, run domain.RunID) {
	if s.cfg.Disabled || !s.enterRun(run) {
		return
	}
	defer s.leaveRun(run)
	s.missionChanges(run, s.integratorNotice(ctx, run))
}

// Only integrators a wake has already seen are offered the change.
func (s *Service) wakeMissionIntegrators(mission domain.MissionID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for run, notice := range s.enhancedNotices {
		if notice.mission == string(mission) {
			s.wakeEnhancedLocked(run)
		}
	}
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
