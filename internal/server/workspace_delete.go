package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/secretstore"
	"github.com/3xDevOps/Aether/internal/store"
)

type workspaceDeletion struct {
	store   *store.DB
	runs    *scheduler.Scheduler
	git     *gitengine.Engine
	bus     events.Bus
	mirrors *mirror.Service
	secrets *secretstore.Store
}

func (d *workspaceDeletion) Delete(ctx context.Context, workspace domain.WorkspaceID, actor domain.MemberID) error {
	return d.runs.WithInactiveWorkspace(ctx, workspace, func(runs []*domain.Run) error {
		if err := d.store.CheckWorkspaceDeletion(ctx, workspace); err != nil {
			return err
		}
		for _, run := range runs {
			if err := d.runs.DeleteRun(ctx, run.ID, actor); err != nil && !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("workspace.delete: delete session %s: %w", run.ID, err)
			}
		}
		if err := d.mirrors.PurgeWorkspace(ctx, workspace); err != nil {
			return fmt.Errorf("workspace.delete: remove mirror: %w", err)
		}
		if err := d.git.RemoveWorkspaceRepo(ctx, workspace); err != nil {
			return err
		}
		// The file never outlives the row, so it goes first; a row that
		// then stays gets it back. An unreadable file has nothing to put
		// back and does not stop the deletion.
		secrets, _ := d.secrets.Get(workspace)
		if err := d.secrets.Delete(workspace); err != nil {
			return fmt.Errorf("workspace.delete: %w", err)
		}
		if err := d.store.DeleteWorkspace(ctx, workspace); err != nil {
			if restoreErr := d.secrets.Put(workspace, secrets); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("workspace.delete: restore the workspace's secrets: %w", restoreErr))
			}
			return err
		}
		if _, err := d.bus.Publish(ctx, events.Event{
			WorkspaceID: workspace, ActorID: actor, Payload: events.WorkspaceDeletedPayload{},
		}); err != nil {
			return fmt.Errorf("workspace deleted but notification failed: %w", err)
		}
		return nil
	})
}
