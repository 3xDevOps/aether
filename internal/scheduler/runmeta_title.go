package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

var runTitleDebounceInterval = 5 * time.Second

type pendingRunTitle struct {
	title       string
	workspaceID domain.WorkspaceID
	timer       *time.Timer
}

// SetRunTitle receives terminal title updates from ptyhost.
func (s *Scheduler) SetRunTitle(run domain.RunID, title string) {
	s.setRunTitle(run, title)
}

func (s *Scheduler) setRunTitle(runID domain.RunID, title string) {
	s.titleMu.Lock()
	if s.titleUpdates == nil {
		s.titleUpdates = make(map[domain.RunID]*pendingRunTitle)
	}
	pending := s.titleUpdates[runID]
	if pending == nil {
		pending = &pendingRunTitle{}
		s.titleUpdates[runID] = pending
	}
	if pending.title == title && pending.timer != nil {
		s.titleMu.Unlock()
		return
	}
	pending.title = title
	if pending.timer == nil {
		pending.timer = time.AfterFunc(runTitleDebounceInterval, func() {
			s.flushRunTitle(runID)
		})
	}
	s.titleMu.Unlock()
}

func (s *Scheduler) flushRunTitle(runID domain.RunID) {
	s.flushRunTitleWithRetry(runID, true)
}

func (s *Scheduler) flushRunTitleWithRetry(runID domain.RunID, retry bool) {
	s.titleMu.Lock()
	pending := s.titleUpdates[runID]
	if pending == nil {
		s.titleMu.Unlock()
		return
	}
	if pending.timer != nil {
		pending.timer.Stop()
		pending.timer = nil
	}
	title := pending.title
	workspaceID := pending.workspaceID
	s.titleMu.Unlock()

	ctx := context.Background()
	changed := true
	if workspaceID == "" {
		run, err := s.cfg.Store.GetRun(ctx, runID)
		if err != nil {
			s.finishRunTitleFlush(runID, err, retry)
			return
		}
		workspaceID = run.WorkspaceID
		changed = run.Title != title
		s.titleMu.Lock()
		if pending := s.titleUpdates[runID]; pending != nil && pending.workspaceID == "" {
			pending.workspaceID = workspaceID
		}
		s.titleMu.Unlock()
	}
	if changed {
		if err := s.cfg.Store.SetRunTitle(ctx, runID, title); err != nil {
			s.finishRunTitleFlush(runID, err, retry)
			return
		}
		s.publish(ctx, events.Event{
			WorkspaceID: workspaceID,
			RunID:       runID,
			Payload:     events.RunTitlePayload{Title: title},
		})
	}

	s.titleMu.Lock()
	pending = s.titleUpdates[runID]
	if pending == nil || !retry || pending.title == title {
		delete(s.titleUpdates, runID)
		s.titleMu.Unlock()
		return
	}
	if pending.timer == nil {
		pending.timer = time.AfterFunc(runTitleDebounceInterval, func() {
			s.flushRunTitle(runID)
		})
	}
	s.titleMu.Unlock()
}

func (s *Scheduler) finishRunTitleFlush(runID domain.RunID, err error, retry bool) {
	slog.Warn("scheduler: persist run title", "run", runID, "error", err)
	s.titleMu.Lock()
	defer s.titleMu.Unlock()
	pending := s.titleUpdates[runID]
	if pending == nil || errors.Is(err, store.ErrNotFound) || !retry {
		delete(s.titleUpdates, runID)
		return
	}
	if pending.timer == nil {
		pending.timer = time.AfterFunc(runTitleDebounceInterval, func() {
			s.flushRunTitle(runID)
		})
	}
}

func (s *Scheduler) flushPendingRunTitles() {
	s.titleMu.Lock()
	ids := make([]domain.RunID, 0, len(s.titleUpdates))
	for runID, pending := range s.titleUpdates {
		if pending.timer != nil {
			pending.timer.Stop()
			pending.timer = nil
		}
		ids = append(ids, runID)
	}
	s.titleMu.Unlock()
	for _, runID := range ids {
		s.flushRunTitleWithRetry(runID, false)
	}
}
