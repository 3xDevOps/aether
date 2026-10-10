package server

import (
	"context"
	"errors"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// publishController runs off the caller's goroutine: the control service
// calls it from paths that may hold store or admission locks.
func (s *Server) publishController(run domain.RunID) {
	go func() {
		s.controllerMu.Lock()
		defer s.controllerMu.Unlock()
		ctx := context.Background()
		r, err := s.db.GetRun(ctx, run)
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			slog.Warn("server: publish run controller", "run", run, "error", err)
			return
		}
		holder, _ := s.control.Status(string(run))
		if _, err := s.bus.Publish(ctx, events.Event{
			WorkspaceID: r.WorkspaceID,
			RunID:       run,
			Payload: events.RunControllerPayload{
				MemberID:     holder.MemberID,
				LastMemberID: s.control.LastHolder(string(run)),
			},
		}); err != nil {
			slog.Warn("server: publish run controller", "run", run, "error", err)
		}
	}()
}
