package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

var runTitleDebounceInterval = 5 * time.Second

type pendingRunTitle struct {
	title string
	timer *time.Timer
}

// SetRunTitle receives terminal title updates from ptyhost.
func (s *Scheduler) SetRunTitle(run domain.RunID, title string) {
	s.setRunTitle(run, title)
}

func (s *Scheduler) setRunTitle(runID domain.RunID, title string) {
	run, err := s.cfg.Store.GetRun(context.Background(), runID)
	if err != nil {
		slog.Warn("scheduler: read run for title", "run", runID, "error", err)
		return
	}

	s.titleMu.Lock()
	if s.titleUpdates == nil {
		s.titleUpdates = make(map[domain.RunID]*pendingRunTitle)
	}
	pending := s.titleUpdates[runID]
	if pending == nil {
		pending = &pendingRunTitle{title: run.Title}
		s.titleUpdates[runID] = pending
	}
	if pending.title == title {
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

	s.publish(context.Background(), events.Event{
		WorkspaceID: run.WorkspaceID,
		RunID:       runID,
		Payload:     events.RunTitlePayload{Title: title},
	})
}

func (s *Scheduler) flushRunTitle(runID domain.RunID) {
	s.titleMu.Lock()
	pending := s.titleUpdates[runID]
	if pending == nil {
		s.titleMu.Unlock()
		return
	}
	pending.timer = nil
	title := pending.title

	run, err := s.cfg.Store.GetRun(context.Background(), runID)
	if err == nil && run.Title != title {
		run.Title = title
		err = s.cfg.Store.UpdateRun(context.Background(), run)
	}
	if err != nil {
		slog.Warn("scheduler: persist run title", "run", runID, "error", err)
		pending.timer = time.AfterFunc(runTitleDebounceInterval, func() {
			s.flushRunTitle(runID)
		})
		s.titleMu.Unlock()
		return
	}
	delete(s.titleUpdates, runID)
	s.titleMu.Unlock()
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
		s.flushRunTitle(runID)
	}
}
