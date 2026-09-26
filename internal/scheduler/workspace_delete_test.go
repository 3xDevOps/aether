package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestWorkspaceDeletionRefusesUnfinishedRunsWithoutStoppingThem(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	run, container := e.launchFake(t, "keep working")
	called := false
	err := e.sched.WithInactiveWorkspace(t.Context(), e.ws.ID, func([]*domain.Run) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrInvalidTransition) || called {
		t.Fatalf("active workspace deletion = %v, cleanup called = %v", err, called)
	}
	fresh, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil || fresh.Status != domain.RunRunning || container.currentState() != "running" {
		t.Fatalf("refused deletion changed live work: %+v, %v, %s", fresh, err, container.currentState())
	}
}

type blockedWorkspaceBase struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockedWorkspaceBase) Capture(context.Context, domain.WorkspaceID, string) (mirror.CaptureResult, error) {
	close(b.entered)
	<-b.release
	return mirror.CaptureResult{}, errors.New("stop after preflight")
}

func TestWorkspaceDeletionCannotRaceLaunchBeforeRunRowExists(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	base := &blockedWorkspaceBase{entered: make(chan struct{}), release: make(chan struct{})}
	e.sched.UseBaseCapture(base)
	done := make(chan error, 1)
	go func() {
		_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "launching", "fake", domain.LaunchHeadless)
		done <- err
	}()
	<-base.entered
	called := false
	err := e.sched.WithInactiveWorkspace(t.Context(), e.ws.ID, func([]*domain.Run) error {
		called = true
		return e.db.DeleteWorkspace(t.Context(), e.ws.ID)
	})
	close(base.release)
	<-done
	if !errors.Is(err, ErrInvalidTransition) || called {
		t.Fatalf("deletion during preflight = %v, cleanup called = %v", err, called)
	}
	if _, err := e.db.GetWorkspace(t.Context(), e.ws.ID); err != nil {
		t.Fatalf("launching workspace was removed: %v", err)
	}
	if err := e.sched.WithInactiveWorkspace(t.Context(), e.ws.ID, func([]*domain.Run) error {
		return e.db.DeleteWorkspace(t.Context(), e.ws.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "late launch", "fake", domain.LaunchHeadless); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("launch after deletion = %v", err)
	}
}
