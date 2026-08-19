package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlay"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// syncAck opens the sync subsystem as signer for run and returns the ack.
func syncAck(t *testing.T, e *testEnv, signer ssh.Signer, run domain.RunID, force bool) protocol.SyncResponse {
	t.Helper()
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	pipe := openSubsystem(t, client, protocol.SubsystemSync, nil)
	header, _ := json.Marshal(protocol.SyncRequest{RunID: string(run), Force: force})
	if _, err := pipe.Write(append(header, '\n')); err != nil {
		t.Fatalf("write header: %v", err)
	}
	var ack protocol.SyncResponse
	readJSONLine(t, bufio.NewReader(pipe), &ack)
	return ack
}

// syncEnv builds a test env whose seeded run has a real worktree dir and
// a non-running status (no --force needed).
func syncEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	e := newTestEnv(t, nil)
	worktree := t.TempDir()
	e.run.Worktree = worktree
	e.run.Status = domain.RunNeedsAttention
	if err := e.store.UpdateRun(context.Background(), e.run); err != nil {
		t.Fatalf("seed worktree: %v", err)
	}
	return e, worktree
}

func TestSyncRefusesInvalidRequests(t *testing.T) {
	e, _ := syncEnv(t)
	if ack := syncAck(t, e, e.signer, "", false); ack.OK || ack.Code != protocol.CodeInvalidParams {
		t.Fatalf("empty run_id ack = %+v, want invalid params", ack)
	}
	if ack := syncAck(t, e, e.signer, "run_missing", false); ack.OK || ack.Code != protocol.CodeNotFound {
		t.Fatalf("unknown run ack = %+v, want not found", ack)
	}
}

func TestSyncRefusesMidWriteUnlessForced(t *testing.T) {
	e, _ := syncEnv(t)
	e.run.Status = domain.RunRunning
	if err := e.store.UpdateRun(context.Background(), e.run); err != nil {
		t.Fatal(err)
	}
	if ack := syncAck(t, e, e.signer, e.run.ID, false); ack.OK || ack.Code != protocol.CodeInvalidState {
		t.Fatalf("mid-write ack = %+v, want invalid state", ack)
	}
	if ack := syncAck(t, e, e.signer, e.run.ID, true); !ack.OK {
		t.Fatalf("forced ack = %+v, want ok", ack)
	}
}

func TestSyncRefusesTerminalRunAndMissingWorktree(t *testing.T) {
	e, _ := syncEnv(t)
	e.run.Status = domain.RunMerged
	now := time.Now().UTC()
	e.run.FinishedAt = &now
	if err := e.store.UpdateRun(context.Background(), e.run); err != nil {
		t.Fatal(err)
	}
	if ack := syncAck(t, e, e.signer, e.run.ID, true); ack.OK || ack.Code != protocol.CodeInvalidState {
		t.Fatalf("terminal run ack = %+v, want invalid state", ack)
	}

	e2 := newTestEnv(t, nil) // seeded run has no worktree
	e2.run.Status = domain.RunNeedsAttention
	if err := e2.store.UpdateRun(context.Background(), e2.run); err != nil {
		t.Fatal(err)
	}
	if ack := syncAck(t, e2, e2.signer, e2.run.ID, false); ack.OK || ack.Code != protocol.CodeUnavailable {
		t.Fatalf("no-worktree ack = %+v, want unavailable", ack)
	}
}

// The Steer capability gates the bridge exactly like PTY writes: viewers
// are denied, collaborators pass, and a protected run shuts
// non-owner-non-admin collaborators out.
func TestSyncGatedOnSteer(t *testing.T) {
	e, _ := syncEnv(t)
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	if ack := syncAck(t, e, viewer, e.run.ID, false); ack.OK || ack.Code != protocol.CodeDenied {
		t.Fatalf("viewer sync ack = %+v, want denied", ack)
	}

	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	if ack := syncAck(t, e, collab, e.run.ID, false); !ack.OK {
		t.Fatalf("collaborator sync ack = %+v, want ok", ack)
	}

	e.run.Protected = true
	if err := e.store.UpdateRun(context.Background(), e.run); err != nil {
		t.Fatal(err)
	}
	if ack := syncAck(t, e, collab, e.run.ID, false); ack.OK || ack.Code != protocol.CodeDenied {
		t.Fatalf("collaborator sync on protected run ack = %+v, want denied", ack)
	}
	// The admin owner still passes.
	if ack := syncAck(t, e, e.signer, e.run.ID, false); !ack.OK {
		t.Fatalf("owner sync on protected run ack = %+v, want ok", ack)
	}
}

// sync.conflict publishes the typed event to both members through the bus.
func TestSyncConflictMethodPublishesEvent(t *testing.T) {
	e, _ := syncEnv(t)
	collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	sub, err := e.bus.Subscribe(context.Background(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeSyncConflict}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	cc := controlAs(t, e, collab)
	if err := cc.Call(protocol.MethodSyncConflict, protocol.SyncConflictParams{
		RunID: string(e.run.ID), SyncSessionID: "sync_test", Files: []string{"f.txt"},
	}, nil); err != nil {
		t.Fatalf("sync.conflict: %v", err)
	}

	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.SyncConflictPayload)
		if !ok {
			t.Fatalf("payload type %T", ev.Payload)
		}
		if p.RunID != e.run.ID || p.SyncSessionID != "sync_test" || len(p.Files) != 1 || p.Files[0] != "f.txt" {
			t.Fatalf("payload = %+v", p)
		}
		// Both members: the reporting collaborator and the run owner.
		if len(p.Members) != 2 || p.Members[0] != cm.ID || p.Members[1] != e.member.ID {
			t.Fatalf("members = %v, want [%s %s]", p.Members, cm.ID, e.member.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no sync.conflict event")
	}

	// A viewer is denied (the method is Steer-gated).
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	vc := controlAs(t, e, viewer)
	wantDenied(t, vc.Call(protocol.MethodSyncConflict, protocol.SyncConflictParams{
		RunID: string(e.run.ID), Files: []string{"f.txt"},
	}, nil), "viewer sync.conflict")
}

// startOverlay builds and starts an overlay session against e's run.
// Returns the session and a channel carrying Run's result.
func startOverlay(t *testing.T, e *testEnv, run domain.RunID, localDir string) (*overlay.Session, context.CancelFunc, <-chan error) {
	t.Helper()
	client, err := e.dialWith(e.signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	// dial runs on mutagen's connection goroutine: it must never call
	// t.Fatal, so the subsystem is opened by hand instead of through the
	// openSubsystem helper.
	dial := func(context.Context) (io.ReadWriteCloser, error) {
		sess, serr := client.NewSession()
		if serr != nil {
			return nil, serr
		}
		stdin, serr := sess.StdinPipe()
		if serr != nil {
			_ = sess.Close()
			return nil, serr
		}
		stdout, serr := sess.StdoutPipe()
		if serr != nil {
			_ = sess.Close()
			return nil, serr
		}
		if serr = sess.RequestSubsystem(protocol.SubsystemSync); serr != nil {
			_ = sess.Close()
			return nil, serr
		}
		pipe := &subsystemPipe{Reader: stdout, stdin: stdin, sess: sess}
		header, _ := json.Marshal(protocol.SyncRequest{RunID: string(run)})
		if _, werr := pipe.Write(append(header, '\n')); werr != nil {
			_ = pipe.Close()
			return nil, werr
		}
		r := bufio.NewReader(pipe)
		line, rerr := protocol.ReadLine(r)
		if rerr != nil {
			_ = pipe.Close()
			return nil, rerr
		}
		var ack protocol.SyncResponse
		if uerr := json.Unmarshal(line, &ack); uerr != nil {
			_ = pipe.Close()
			return nil, uerr
		}
		if !ack.OK {
			_ = pipe.Close()
			return nil, errors.New("sync refused: " + ack.Error)
		}
		return &bufferedPipe{r: r, pipe: pipe}, nil
	}
	sess, err := overlay.NewSession(overlay.Options{LocalDir: localDir, Dial: dial, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("overlay.NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := sess.Start(ctx, string(run)); err != nil {
		t.Fatalf("overlay.Start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Run(ctx) }()
	return sess, cancel, done
}

// bufferedPipe keeps post-ack bytes buffered in the ack reader visible to
// the raw stream (mirrors cli.bufferedStream).
type bufferedPipe struct {
	r    *bufio.Reader
	pipe *subsystemPipe
}

func (b *bufferedPipe) Read(p []byte) (int, error)  { return b.r.Read(p) }
func (b *bufferedPipe) Write(p []byte) (int, error) { return b.pipe.Write(p) }
func (b *bufferedPipe) Close() error                { return b.pipe.Close() }

// waitForFile polls until path has the wanted content or the deadline
// passes.
func waitForFile(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && string(b) == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, err := os.ReadFile(path)
	t.Fatalf("%s = %q (err %v), want %q", path, b, err, want)
}

// waitForPath polls until path exists or the deadline passes.
func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

// End-to-end overlay through the real subsystem: local edits propagate to
// the worktree and worktree edits propagate back.
func TestSyncOverlayPropagatesBothWays(t *testing.T) {
	e, worktree := syncEnv(t)
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "local.txt"), []byte("from local"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, cancel, done := startOverlay(t, e, e.run.ID, localDir)

	// Local -> worktree.
	waitForFile(t, filepath.Join(worktree, "local.txt"), "from local")

	// Worktree -> local.
	if err := os.WriteFile(filepath.Join(worktree, "remote.txt"), []byte("from run"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sess.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	waitForFile(t, filepath.Join(localDir, "remote.txt"), "from run")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("overlay run: %v", err)
	}
}

// Concurrent conflicting edits pause the overlay, preserve the local side
// as a conflict twin, and leave the worktree version canonical.
func TestSyncOverlayConflictPausesAndPreservesTwin(t *testing.T) {
	e, worktree := syncEnv(t)
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "shared.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, cancel, done := startOverlay(t, e, e.run.ID, localDir)
	defer cancel()
	waitForFile(t, filepath.Join(worktree, "shared.txt"), "base")

	// Freeze propagation between the conflicting writes by pausing:
	// without a real barrier, one side could win a race and no conflict
	// would exist. Rewriting both sides between Pause and Resume makes
	// the conflict deterministic.
	if err := sess.Pause(context.Background()); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "shared.txt"), []byte("local wins?"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "shared.txt"), []byte("run wins"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sess.Resume(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}

	err := <-done
	var conflict *overlay.Conflict
	if !errors.As(err, &conflict) {
		t.Fatalf("overlay run = %v, want *overlay.Conflict", err)
	}
	if len(conflict.Files) != 1 || conflict.Files[0] != "shared.txt" {
		t.Fatalf("conflict files = %v, want [shared.txt]", conflict.Files)
	}
	if !sess.Paused(context.Background()) {
		t.Fatal("session not paused after conflict")
	}

	twin, terr := os.ReadFile(filepath.Join(localDir, "shared.txt"+overlay.ConflictSuffix))
	if terr != nil {
		t.Fatalf("read twin: %v", terr)
	}
	if string(twin) != "local wins?" {
		t.Fatalf("twin = %q, want the losing local edit", twin)
	}
	// The worktree side stays canonical.
	wb, werr := os.ReadFile(filepath.Join(worktree, "shared.txt"))
	if werr != nil || string(wb) != "run wins" {
		t.Fatalf("worktree content = %q (err %v), want %q", wb, werr, "run wins")
	}
}

// The overlay never touches version control state: a .git directory in
// the worktree is invisible to the sync.
func TestSyncOverlayIgnoresGitState(t *testing.T) {
	e, worktree := syncEnv(t)
	gitDir := filepath.Join(worktree, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, cancel, done := startOverlay(t, e, e.run.ID, localDir)
	waitForFile(t, filepath.Join(worktree, "f.txt"), "x")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("overlay run: %v", err)
	}

	// .git never propagated to the local side...
	if _, err := os.Stat(filepath.Join(localDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git leaked to local dir (err %v)", err)
	}
	// ...and the worktree's .git is untouched.
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil || string(head) != "ref: refs/heads/main\n" {
		t.Fatalf(".git/HEAD = %q (err %v), want untouched", head, err)
	}
}
