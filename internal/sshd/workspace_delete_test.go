package sshd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Exercise the SSH admission gate with real row and filesystem cleanup. The
// production callback additionally checks scheduler/runtime state; these tests
// deliberately leave that boundary idle so a transport is the only blocker.
func workspaceDeletionEnv(t *testing.T, mod func(*Config)) (*testEnv, *gitengine.Engine) {
	t.Helper()
	dir := t.TempDir()
	git, err := gitengine.New(gitengine.Config{ReposDir: filepath.Join(dir, "repos"), CheckoutsDir: filepath.Join(dir, "checkouts")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = git.Close() })
	e := newTestEnv(t, func(c *Config) {
		st := c.Store
		c.Git = git
		c.Services.Files = git
		c.DeleteWorkspace = func(ctx context.Context, id domain.WorkspaceID, _ domain.MemberID) error {
			runs, err := st.ListRunsByWorkspace(ctx, id)
			if err != nil {
				return err
			}
			for _, run := range runs {
				if !run.Status.Terminal() {
					return fmt.Errorf("%w: unfinished run %s", store.ErrInUse, run.ID)
				}
			}
			for _, run := range runs {
				if err := os.RemoveAll(run.Worktree); err != nil {
					return err
				}
				if err := st.DeleteRun(ctx, run.ID); err != nil {
					return err
				}
			}
			if err := git.RemoveWorkspaceRepo(ctx, id); err != nil {
				return err
			}
			return st.DeleteWorkspace(ctx, id)
		}
		if mod != nil {
			mod(c)
		}
	})
	return e, git
}

func deletionOtherWorkspace(t *testing.T, e *testEnv) *domain.Workspace {
	t.Helper()
	ws := &domain.Workspace{Name: "other", BaseBranch: "main", Environment: domain.WorkspaceEnvironment{}}
	if err := e.store.CreateWorkspace(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func finishDeletionRun(t *testing.T, e *testEnv) {
	t.Helper()
	now := time.Now().UTC()
	if err := e.store.UpdateRunStatus(t.Context(), e.run.ID, domain.RunMerged, "", nil, &now); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceDeleted(t *testing.T, e *testEnv, id domain.WorkspaceID) {
	t.Helper()
	if _, err := e.store.GetWorkspace(t.Context(), id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("workspace after deletion: %v, want not found", err)
	}
}

func TestWorkspaceDeleteDuringLiveSyncTargetsOnlyItsWorkspace(t *testing.T) {
	// Real Mutagen watchers, like the other live overlay tests, are not parallel.
	e, _ := workspaceDeletionEnv(t, func(c *Config) { c.revalidateInterval = time.Hour })
	worktree := t.TempDir()
	e.run.Worktree = worktree
	e.run.Status = domain.RunNeedsAttention
	if err := e.store.UpdateRun(t.Context(), e.run); err != nil {
		t.Fatal(err)
	}
	other := deletionOtherWorkspace(t, e)
	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "live.txt"), []byte("before deletion"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, cancel, done := startOverlay(t, e, e.run.ID, local)
	waitForFile(t, filepath.Join(worktree, "live.txt"), "before deletion")
	// The run has finished, but the already-serving endpoint still owns files
	// until its session closes. Do not let the periodic revoker hide that race.
	finishDeletionRun(t, e)
	admin := controlClient(t, e)
	collaborator, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	wantDenied(t, controlAs(t, e, collaborator).Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(e.ws.ID)}, nil), "non-admin workspace.delete during sync")
	if err := admin.Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(other.ID)}, nil); err != nil {
		t.Fatalf("delete unrelated workspace during live sync: %v", err)
	}
	assertWorkspaceDeleted(t, e, other.ID)
	if pe := wireErrOf(t, admin.Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(e.ws.ID)}, nil)); pe.Code != protocol.CodeConflict {
		t.Fatalf("delete live overlay workspace = %+v, want conflict", pe)
	}
	if err := os.WriteFile(filepath.Join(local, "still-live.txt"), []byte("after refusal"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sess.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, filepath.Join(worktree, "still-live.txt"), "after refusal")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sess.Close()
	// Client shutdown precedes the asynchronous SSH handler's unwind. Retry
	// only its explicit busy response, with a bounded teardown deadline.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := admin.Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(e.ws.ID)}, nil)
		if err == nil {
			break
		}
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != protocol.CodeConflict || time.Now().After(deadline) {
			t.Fatalf("delete after overlay closes: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertWorkspaceDeleted(t, e, e.ws.ID)
	if _, err := e.store.GetRun(t.Context(), e.run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("run after deletion: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree after deletion: %v", err)
	}
	if ack := syncAck(t, e, e.signer, e.run.ID, true); ack.OK || ack.Code != protocol.CodeNotFound {
		t.Fatalf("new sync after deletion = %+v, want not found", ack)
	}
}

func TestWorkspaceDeleteDuringGitTransportTargetsOnlyItsWorkspace(t *testing.T) {
	for _, op := range []string{"upload-pack", "receive-pack"} {
		t.Run(op, func(t *testing.T) {
			e, git := workspaceDeletionEnv(t, nil)
			finishDeletionRun(t, e)
			other := deletionOtherWorkspace(t, e)
			repo, err := git.InitWorkspaceRepo(t.Context(), e.ws.ID)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := e.dial(t).NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close() //nolint:errcheck
			stdin, err := sess.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := sess.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.Start(fmt.Sprintf("git-%s '/%s.git'", op, e.ws.ID)); err != nil {
				t.Fatal(err)
			}
			// A real pack process has advertised, but is still waiting for the
			// client's request. Keep its stdin open throughout both deletions.
			var prefix [4]byte
			if _, err := io.ReadFull(stdout, prefix[:]); err != nil {
				t.Fatal(err)
			}
			admin := controlClient(t, e)
			if err := admin.Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(other.ID)}, nil); err != nil {
				t.Fatalf("delete unrelated workspace during git: %v", err)
			}
			assertWorkspaceDeleted(t, e, other.ID)
			if pe := wireErrOf(t, admin.Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(e.ws.ID)}, nil)); pe.Code != protocol.CodeConflict {
				t.Fatalf("delete git workspace = %+v, want conflict", pe)
			}
			if _, err := io.WriteString(stdin, "0000"); err != nil {
				t.Fatal(err)
			}
			if err := stdin.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, stdout); err != nil {
				t.Fatal(err)
			}
			if err := sess.Wait(); err != nil {
				t.Fatal(err)
			}
			if err := admin.Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(e.ws.ID)}, nil); err != nil {
				t.Fatalf("delete after git finishes: %v", err)
			}
			assertWorkspaceDeleted(t, e, e.ws.ID)
			if code, stderr := gitExecAs(t, e, e.signer, fmt.Sprintf("git-%s '/%s.git'", op, e.ws.ID)); code != 128 {
				t.Fatalf("git after deletion = %d (%s), want 128", code, stderr)
			}
			if _, err := os.Stat(repo); !os.IsNotExist(err) {
				t.Fatalf("repository resurrected after deletion: %v", err)
			}
		})
	}
}

type blockedDeletionDisk struct {
	started chan struct{}
	release chan struct{}
}

func (d *blockedDeletionDisk) Usage() (disk.Usage, error) {
	close(d.started)
	<-d.release
	return disk.Usage{TotalBytes: 100, UsedBytes: 25}, nil
}

func TestWorkspaceDeleteDoesNotWaitForUnrelatedControlRead(t *testing.T) {
	t.Parallel()
	d := &blockedDeletionDisk{started: make(chan struct{}), release: make(chan struct{})}
	e, _ := workspaceDeletionEnv(t, func(c *Config) { c.Services.Disk = d })
	release := sync.OnceFunc(func() { close(d.release) })
	defer release()
	other := deletionOtherWorkspace(t, e)
	reader := controlClient(t, e)
	done := make(chan error, 1)
	go func() { done <- reader.Call(protocol.MethodServerDisk, nil, nil) }()
	select {
	case <-d.started:
	case <-time.After(5 * time.Second):
		t.Fatal("control read did not start")
	}
	if err := controlClient(t, e).Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(other.ID)}, nil); err != nil {
		t.Fatalf("delete during unrelated control read: %v", err)
	}
	assertWorkspaceDeleted(t, e, other.ID)
	select {
	case err := <-done:
		t.Fatalf("blocked read finished before release: %v", err)
	default:
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("control read after deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control read did not finish")
	}
}

type deletionDiscoveryStore struct {
	store.Store
	store.HandoffOutboxStore
	workspace bool
	paused    atomic.Bool
	found     chan struct{}
	release   chan struct{}
}

func (s *deletionDiscoveryStore) pause(ctx context.Context) error {
	if !s.paused.CompareAndSwap(false, true) {
		return nil
	}
	close(s.found)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *deletionDiscoveryStore) GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	run, err := s.Store.GetRun(ctx, id)
	if err == nil && !s.workspace {
		err = s.pause(ctx)
	}
	return run, err
}

func (s *deletionDiscoveryStore) GetWorkspace(ctx context.Context, id domain.WorkspaceID) (*domain.Workspace, error) {
	ws, err := s.Store.GetWorkspace(ctx, id)
	if err == nil && s.workspace {
		err = s.pause(ctx)
	}
	return ws, err
}

func TestWorkspaceDeleteWinsTargetDiscoveryRace(t *testing.T) {
	for _, name := range []string{"sync", "guarded-files", "git"} {
		t.Run(name, func(t *testing.T) {
			barrier := &deletionDiscoveryStore{workspace: name != "sync", found: make(chan struct{}), release: make(chan struct{})}
			e, git := workspaceDeletionEnv(t, func(c *Config) {
				barrier.Store = c.Store
				barrier.HandoffOutboxStore = c.Store.(store.HandoffOutboxStore)
				c.Store = barrier
			})
			repo, err := git.InitWorkspaceRepo(t.Context(), e.ws.ID)
			if err != nil {
				t.Fatal(err)
			}
			worktree := t.TempDir()
			e.run.Worktree = worktree
			e.run.Status = domain.RunNeedsAttention
			if err = e.store.UpdateRun(t.Context(), e.run); err != nil {
				t.Fatal(err)
			}
			var result <-chan error
			var pipe *subsystemPipe
			switch name {
			case "guarded-files":
				client := controlClient(t, e)
				done := make(chan error, 1)
				result = done
				go func() {
					done <- client.Call(protocol.MethodFilesTree, protocol.FilesTreeParams{WorkspaceID: string(e.ws.ID)}, nil)
				}()
			case "git":
				sess, sessionErr := e.dial(t).NewSession()
				if sessionErr != nil {
					t.Fatal(sessionErr)
				}
				defer sess.Close() //nolint:errcheck
				done := make(chan error, 1)
				result = done
				go func() { done <- sess.Run(fmt.Sprintf("git-receive-pack '/%s.git'", e.ws.ID)) }()
			default:
				pipe = openSubsystem(t, e.dial(t), protocol.SubsystemSync, nil)
				if err = writeJSONLine(pipe, protocol.SyncRequest{RunID: string(e.run.ID)}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-barrier.found:
			case <-time.After(5 * time.Second):
				t.Fatal("target discovery did not start")
			}
			finishDeletionRun(t, e)
			err = controlClient(t, e).Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(e.ws.ID)}, nil)
			close(barrier.release)
			if err != nil {
				t.Fatalf("delete before target admission: %v", err)
			}
			switch name {
			case "guarded-files":
				select {
				case err := <-result:
					if pe := wireErrOf(t, err); pe.Code != protocol.CodeNotFound {
						t.Fatalf("stale files request = %+v, want not found", pe)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("stale files request did not finish")
				}
			case "git":
				select {
				case err := <-result:
					var exitErr *ssh.ExitError
					if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 128 {
						t.Fatalf("stale git request = %v, want exit status 128", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("stale git request did not finish")
				}
			default:
				var ack protocol.SyncResponse
				readJSONLine(t, bufio.NewReader(pipe), &ack)
				if ack.OK || ack.Code != protocol.CodeNotFound {
					t.Fatalf("stale sync request = %+v, want not found", ack)
				}
			}
			assertWorkspaceDeleted(t, e, e.ws.ID)
			for _, path := range []string{worktree, repo} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("resource resurrected after deletion: %s: %v", path, err)
				}
			}
			if _, err := e.store.GetRun(t.Context(), e.run.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("run resurrected after deletion: %v", err)
			}
		})
	}
}
