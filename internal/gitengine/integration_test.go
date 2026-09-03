//go:build integration

// Integration tests against the real git CLI. The transport tests drive
// Engine.UploadPack / ReceivePack with genuine `git clone/fetch/push`
// invocations: an `ext::` remote re-executes this test binary in bridge
// mode (see TestMain), which relays the pack protocol over TCP to an
// in-process handler - an ssh-less stand-in for the SSH exec channel.
package gitengine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

const bridgeEnv = "GITENGINE_TEST_BRIDGE"

func TestMain(m *testing.M) {
	if os.Getenv(bridgeEnv) == "1" {
		os.Exit(runBridge())
	}
	os.Exit(m.Run())
}

// runBridge is the client side of the transport harness. git invokes this
// binary via an ext:: remote as `<exe> <addr> <service> <workspace-id>`;
// it relays stdio to the test process's TCP transport server.
func runBridge() int {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "bridge: bad argv")
		return 1
	}
	addr, service, ws := os.Args[1], os.Args[2], os.Args[3]
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bridge:", err)
		return 1
	}
	defer func() { _ = conn.Close() }()
	header, _ := json.Marshal(map[string]string{"service": service, "ws": ws})
	if _, werr := conn.Write(append(header, '\n')); werr != nil {
		return 1
	}
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		_ = conn.(*net.TCPConn).CloseWrite()
	}()
	_, _ = io.Copy(os.Stdout, conn)
	return 0
}

// serveTransport runs a TCP server dispatching bridge connections into the
// engine's pack handlers, and returns an ext:: remote URL factory.
func serveTransport(t *testing.T, e *Engine) func(ws domain.WorkspaceID) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				br := bufio.NewReader(conn)
				line, readErr := br.ReadString('\n')
				if readErr != nil {
					return
				}
				var h struct{ Service, WS string }
				if json.Unmarshal([]byte(line), &h) != nil {
					return
				}
				ctx := t.Context()
				switch h.Service {
				case "git-upload-pack":
					_, _ = e.UploadPack(ctx, domain.WorkspaceID(h.WS), br, conn, os.Stderr)
				case "git-receive-pack":
					_, _ = e.ReceivePack(ctx, domain.WorkspaceID(h.WS), br, conn, os.Stderr)
				}
				_ = conn.(*net.TCPConn).CloseWrite()
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	return func(ws domain.WorkspaceID) string {
		return "ext::" + exe + " " + ln.Addr().String() + " %S " + string(ws)
	}
}

// gitc runs the client-side git CLI in dir.
func gitc(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{
		"-c", "protocol.ext.allow=always",
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"-c", "init.defaultBranch=main",
	}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		bridgeEnv+"=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newTestEngine(t *testing.T, bus events.Bus) *Engine {
	t.Helper()
	dir := t.TempDir()
	e, err := New(Config{
		ReposDir:     filepath.Join(dir, "repos"),
		CheckoutsDir: filepath.Join(dir, "checkouts"),
		Bus:          bus,
		QuietPeriod:  100 * time.Millisecond,
		MinInterval:  150 * time.Millisecond,
		MaxInterval:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// seedWorkspace inits the workspace bare repo and imports an initial commit
// on main into it via a client-side clone + push through the transport.
func seedWorkspace(t *testing.T, e *Engine, url func(domain.WorkspaceID) string, ws domain.WorkspaceID) {
	t.Helper()
	if _, err := e.InitWorkspaceRepo(t.Context(), ws); err != nil {
		t.Fatalf("InitWorkspaceRepo: %v", err)
	}
	src := t.TempDir()
	gitc(t, src, "init")
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, src, "add", "-A")
	gitc(t, src, "commit", "-m", "initial")
	gitc(t, src, "push", url(ws), "main")
}

func bareRevParse(t *testing.T, e *Engine, ws domain.WorkspaceID, ref string) string {
	t.Helper()
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		t.Fatalf("repo path: %v", err)
	}
	out, err := e.git(t.Context(), repo, "rev-parse", "--verify", ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return out
}

func TestTransportImportPushFetch(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)

	// Import: client push into the empty bare repo through ReceivePack.
	seedWorkspace(t, e, url, "wsimport")
	if got := bareRevParse(t, e, "wsimport", "refs/heads/main"); len(got) != 40 {
		t.Fatalf("main not imported, rev-parse = %q", got)
	}

	// Fetch: real git clone through UploadPack.
	dst := t.TempDir()
	gitc(t, dst, "clone", url("wsimport"), "clone")
	data, err := os.ReadFile(filepath.Join(dst, "clone", "file.txt"))
	if err != nil || string(data) != "one\ntwo\n" {
		t.Fatalf("cloned content = %q, %v", data, err)
	}

	// Incremental: another push, then fetch it back.
	if err := os.WriteFile(filepath.Join(dst, "clone", "file.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cloneDir := filepath.Join(dst, "clone")
	gitc(t, cloneDir, "commit", "-am", "more")
	gitc(t, cloneDir, "push", url("wsimport"), "main")
	want := gitc(t, cloneDir, "rev-parse", "HEAD")
	if got := bareRevParse(t, e, "wsimport", "refs/heads/main"); got != want {
		t.Fatalf("bare main = %s, want %s", got, want)
	}

	dst2 := t.TempDir()
	gitc(t, dst2, "clone", url("wsimport"), "clone2")
	if got := gitc(t, filepath.Join(dst2, "clone2"), "rev-parse", "HEAD"); got != want {
		t.Fatalf("second clone HEAD = %s, want %s", got, want)
	}
}

func TestCheckoutLifecycle(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, branch, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "Fix the Auth bug!")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if branch != "aether/run-fix-the-auth-bug-run1" {
		t.Errorf("branch = %q", branch)
	}
	if want := filepath.Join(e.cfg.CheckoutsDir, "run1"); checkout != want {
		t.Errorf("checkout = %q, want %q", checkout, want)
	}
	if _, statErr := os.Stat(filepath.Join(checkout, ".git", "config")); statErr != nil {
		t.Fatalf("checkout .git is not self-contained: %v", statErr)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")
	if got, _ := e.git(ctx, checkout, "config", cfgBase); got != base {
		t.Errorf("aether.base = %q, want %q", got, base)
	}

	// Duplicate create and bad base branch both fail.
	if _, _, dupErr := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "x"); dupErr == nil {
		t.Error("duplicate CreateRunCheckout should fail")
	}
	if _, _, baseErr := e.CreateRunCheckout(ctx, "ws1", "run2", "no-such-branch", "x"); baseErr == nil {
		t.Error("CreateRunCheckout from unborn base should fail")
	}

	// Clean tree: CommitAll is a no-op.
	if noop, noopErr := e.CommitAll(ctx, "run1", "aether: noop"); noopErr != nil || noop != "" {
		t.Fatalf("clean CommitAll = (%q, %v), want (\"\", nil)", noop, noopErr)
	}

	// Dirty tree: wip commit with the fixed identity.
	if writeErr := os.WriteFile(filepath.Join(checkout, "new.txt"), []byte("hi\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	commit, err := e.CommitAll(ctx, "run1", "wip: fix the auth bug")
	if err != nil || len(commit) != 40 {
		t.Fatalf("CommitAll = (%q, %v)", commit, err)
	}
	if author, _ := e.git(ctx, checkout, "log", "-1", "--format=%an <%ae>"); author != "Aether <aether@localhost>" {
		t.Errorf("commit author = %q", author)
	}

	// Publish makes the branch fetchable from the bare repo.
	tip, err := e.PublishRunBranch(ctx, "run1")
	if err != nil || tip != commit {
		t.Fatalf("PublishRunBranch = (%q, %v), want %q", tip, err, commit)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+branch); got != commit {
		t.Fatalf("bare branch tip = %s, want %s", got, commit)
	}

	// A client can fetch the run branch through the transport.
	dst := t.TempDir()
	gitc(t, dst, "clone", "--branch", branch, url("ws1"), "c")
	if got := gitc(t, filepath.Join(dst, "c"), "rev-parse", "HEAD"); got != commit {
		t.Fatalf("fetched run branch tip = %s, want %s", got, commit)
	}

	// Removal deletes the checkout, never the branch; idempotent.
	if err := e.RemoveRunCheckout(ctx, "run1"); err != nil {
		t.Fatalf("RemoveRunCheckout: %v", err)
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("checkout still exists: %v", err)
	}
	if err := e.RemoveRunCheckout(ctx, "run1"); err != nil {
		t.Fatalf("second RemoveRunCheckout: %v", err)
	}
	if _, err := os.Stat(checkout + ".json"); !os.IsNotExist(err) {
		t.Fatalf("identity sidecar survived checkout removal: %v", err)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+branch); got != commit {
		t.Fatalf("branch lost after checkout removal: %s", got)
	}
}

// Branch names lead with the task and carry only a short tail of the run
// ID, so two runs of the same task can land on the same name. The name is
// reserved at checkout rather than at publish, so the second run falls
// back to the full ID even while the first is still unpublished. Without
// the reservation both would take the short name and publication, which
// force-updates the ref, would silently overwrite the first run's branch.
func TestRunBranchFallsBackToTheFullIDOnCollision(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	const first = "01m0h6tym4y65102a721nq0jf3"
	const colliding = "01m0aaaaaaaaaaaaaaaaanq0jf3" // same last six characters
	_, firstBranch, err := e.CreateRunCheckout(ctx, "ws1", first, "main", "Fix the Auth bug!")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if firstBranch != "aether/run-fix-the-auth-bug-nq0jf3" {
		t.Fatalf("first branch = %q, want the short-id form", firstBranch)
	}

	_, branch, err := e.CreateRunCheckout(ctx, "ws1", colliding, "main", "Fix the Auth bug!")
	if err != nil {
		t.Fatalf("colliding run: %v", err)
	}
	if want := "aether/run-fix-the-auth-bug-" + colliding; branch != want {
		t.Errorf("colliding branch = %q, want %q", branch, want)
	}

	// Both runs publish independently: the reservation kept them on
	// separate refs, so neither overwrites the other.
	firstTip, err := e.PublishRunBranch(ctx, first)
	if err != nil {
		t.Fatalf("publish first run: %v", err)
	}
	if _, err := e.PublishRunBranch(ctx, colliding); err != nil {
		t.Fatalf("publish colliding run: %v", err)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+firstBranch); got != firstTip {
		t.Errorf("first run's branch tip = %s after the second published, want %s", got, firstTip)
	}
}

// A launch that fails after the branch name is reserved must give the name
// back, or the next run of that task is pushed onto the full-ID form for
// good.
func TestFailedCheckoutReleasesItsBranchName(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	const run = "01m0h6tym4y65102a721nq0jf3"
	const short = "aether/run-fix-the-auth-bug-nq0jf3"

	// Fail the run-meta write, which happens after the branch is reserved.
	// The sidecar lives at <CheckoutsDir>/<run>.json, so a directory
	// already sitting on that exact path lets the clone and the branch
	// reservation succeed and then stops the sidecar from being written.
	sidecar := filepath.Join(e.cfg.CheckoutsDir, string(run)+".json")
	if err := os.MkdirAll(sidecar, 0o700); err != nil {
		t.Fatalf("occupy the sidecar path: %v", err)
	}
	_, _, err := e.CreateRunCheckout(ctx, "ws1", run, "main", "Fix the Auth bug!")
	if err == nil {
		t.Fatal("checkout with an unwritable sidecar path succeeded, want a failure")
	}
	if err := os.RemoveAll(sidecar); err != nil {
		t.Fatalf("free the sidecar path: %v", err)
	}

	if exists, existsErr := e.WorkspaceBranchExists(ctx, "ws1", short); existsErr != nil {
		t.Fatalf("WorkspaceBranchExists: %v", existsErr)
	} else if exists {
		t.Fatal("a failed checkout left its branch name reserved")
	}
	// The name is free, so a retry gets the readable form back.
	_, branch, err := e.CreateRunCheckout(ctx, "ws1", run, "main", "Fix the Auth bug!")
	if err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	if branch != short {
		t.Errorf("retry branch = %q, want %q", branch, short)
	}
}

// TestAgentCannotRedirectPublish is the SBP-001 attack: the run checkout is
// bind-mounted into the container, so the agent owns its .git/config. It
// rewrites aether.branch/aether.workspace to point at another workspace's
// main and commits hostile content. Publication must follow the server's
// own identity record, leaving both mains untouched.
func TestAgentCannotRedirectPublish(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	seedWorkspace(t, e, url, "ws2")
	ctx := t.Context()

	checkout, branch, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "attack")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(checkout, "run1.json")); err == nil {
		t.Fatal("identity sidecar is inside the bind-mounted checkout")
	}
	victim1 := bareRevParse(t, e, "ws1", "refs/heads/main")
	victim2 := bareRevParse(t, e, "ws2", "refs/heads/main")

	// The agent rewrites the checkout's own config and commits.
	for key, val := range map[string]string{cfgBranch: "main", cfgWorkspace: "ws2", cfgBase: "HEAD"} {
		if _, err := e.git(ctx, checkout, "config", key, val); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(checkout, "hostile.txt"), []byte("pwned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostile, err := e.CommitAll(ctx, "run1", "hostile")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.PublishRunBranch(ctx, "run1"); err != nil {
		t.Fatalf("PublishRunBranch: %v", err)
	}
	if got := bareRevParse(t, e, "ws2", "refs/heads/main"); got != victim2 {
		t.Fatalf("ws2 main = %s after attack, want %s (agent hijacked another workspace)", got, victim2)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/main"); got != victim1 {
		t.Fatalf("ws1 main = %s after attack, want %s (agent hijacked the workspace default branch)", got, victim1)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+branch); got != hostile {
		t.Fatalf("run branch tip = %s, want the run's own commit %s", got, hostile)
	}

	// A checkout with no identity record (created before the sidecar
	// existed) publishes nothing rather than trusting the checkout.
	if err := os.Remove(checkout + ".json"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PublishRunBranch(ctx, "run1"); err == nil {
		t.Fatal("PublishRunBranch accepted a checkout with no identity record")
	}
	if err := e.StartDiffWatch(ctx, "ws1", "run1"); err == nil {
		e.StopDiffWatch("run1")
		t.Fatal("StartDiffWatch accepted a checkout with no identity record")
	}
}

func TestConcurrentRunsOneWorkspace(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run := domain.RunID(fmt.Sprintf("run%d", i))
			checkout, _, err := e.CreateRunCheckout(ctx, "ws1", run, "main", fmt.Sprintf("task %d", i))
			if err != nil {
				errs <- fmt.Errorf("%s create: %w", run, err)
				return
			}
			if err := os.WriteFile(filepath.Join(checkout, fmt.Sprintf("f%d.txt", i)), []byte("x\n"), 0o644); err != nil {
				errs <- err
				return
			}
			if _, err := e.CommitAll(ctx, run, "aether: work"); err != nil {
				errs <- fmt.Errorf("%s commit: %w", run, err)
				return
			}
			if _, err := e.PublishRunBranch(ctx, run); err != nil {
				errs <- fmt.Errorf("%s publish: %w", run, err)
				return
			}
			if err := e.RemoveRunCheckout(ctx, run); err != nil {
				errs <- fmt.Errorf("%s remove: %w", run, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for i := range n {
		branch := fmt.Sprintf("refs/heads/aether/run-task-%d-run%d", i, i)
		if got := bareRevParse(t, e, "ws1", branch); len(got) != 40 {
			t.Errorf("branch %s missing after concurrent runs", branch)
		}
	}
}

// subscribeTypes subscribes to the bus for the given event types.
func subscribeTypes(t *testing.T, bus events.Bus, types ...events.Type) events.Subscription {
	t.Helper()
	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{Filter: events.Filter{Types: types}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}

func nextEvent(t *testing.T, sub events.Subscription, timeout time.Duration) (events.Event, bool) {
	t.Helper()
	select {
	case e, ok := <-sub.Events():
		return e, ok
	case <-time.After(timeout):
		return events.Event{}, false
	}
}

func TestDiffWatchQuiescence(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, branch, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "watch me")
	if err != nil {
		t.Fatal(err)
	}
	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	branches := subscribeTypes(t, bus, events.TypeGitBranch)
	if err := e.StartDiffWatch(ctx, "ws1", "run1"); err != nil {
		t.Fatalf("StartDiffWatch: %v", err)
	}
	if err := e.StartDiffWatch(ctx, "ws1", "run1"); err != nil {
		t.Fatalf("StartDiffWatch twice: %v", err)
	}

	// An untracked write fires a run.diff event after quiescence.
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, ok := nextEvent(t, diffs, 5*time.Second)
	if !ok {
		t.Fatal("no run.diff event after write")
	}
	if ev.WorkspaceID != "ws1" || ev.RunID != "run1" {
		t.Fatalf("event scope = %s/%s", ev.WorkspaceID, ev.RunID)
	}
	files := ev.Payload.(events.RunDiffPayload).Files
	if len(files) != 1 || files[0] != (events.FileDiffStat{Path: "notes.txt", Additions: 2}) {
		t.Fatalf("diff files = %+v", files)
	}
	if when, changed := e.LastFileChange("run1"); !changed || time.Since(when) > time.Minute {
		t.Fatalf("LastFileChange = %v, %v", when, changed)
	}

	// Quiet tree: no further events.
	if extra, more := nextEvent(t, diffs, 600*time.Millisecond); more {
		t.Fatalf("unexpected run.diff while quiet: %+v", extra.Payload)
	}

	// Tracked edit in a subdirectory joins the stat set.
	if err := os.MkdirAll(filepath.Join(checkout, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "sub", "deep.txt"), []byte("z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "file.txt"), []byte("one\nCHANGED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The writes may land in one snapshot or several; the last event before
	// the tree goes quiet must reflect the final state.
	var got map[string]events.FileDiffStat
	deadline := time.Now().Add(5 * time.Second)
	for len(got) != 3 && time.Now().Before(deadline) {
		ev, ok = nextEvent(t, diffs, 2*time.Second)
		if !ok {
			break
		}
		got = map[string]events.FileDiffStat{}
		for _, f := range ev.Payload.(events.RunDiffPayload).Files {
			got[f.Path] = f
		}
	}
	if len(got) != 3 {
		t.Fatalf("final diff files = %+v", got)
	}
	if got["file.txt"] != (events.FileDiffStat{Path: "file.txt", Additions: 1, Deletions: 1}) {
		t.Errorf("file.txt stat = %+v", got["file.txt"])
	}
	if got["sub/deep.txt"] != (events.FileDiffStat{Path: "sub/deep.txt", Additions: 1}) {
		t.Errorf("sub/deep.txt stat = %+v", got["sub/deep.txt"])
	}

	// The final event reflects the final state: nothing further while quiet.
	if extra, more := nextEvent(t, diffs, 600*time.Millisecond); more {
		t.Fatalf("unexpected run.diff while quiet: %+v", extra.Payload)
	}

	// An agent-side commit moves HEAD; the next snapshot publishes the run
	// branch into the bare repo and emits git.branch. Host-side git stands
	// in for the agent's in-container git.
	if _, err := e.git(ctx, checkout, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "-c", "user.name=Agent", "-c", "user.email=a@x", "commit", "-m", "agent work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "after.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bev, ok := nextEvent(t, branches, 5*time.Second)
	if !ok {
		t.Fatal("no git.branch event after agent commit")
	}
	pl := bev.Payload.(events.GitBranchPayload)
	head, _ := e.git(ctx, checkout, "rev-parse", "HEAD")
	if pl.WorkspaceID != "ws1" || pl.Branch != branch || pl.Commit != head {
		t.Fatalf("git.branch payload = %+v, want ws1/%s/%s", pl, branch, head)
	}
	if bev.WorkspaceID != "ws1" || bev.RunID != "run1" {
		t.Fatalf("git.branch scope = %s/%s", bev.WorkspaceID, bev.RunID)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+branch); got != head {
		t.Fatalf("bare branch tip = %s, want %s", got, head)
	}

	// Drain the run.diff snapshot that accompanied the commit before
	// checking that a stopped watch stays silent.
	for {
		if _, ok := nextEvent(t, diffs, 600*time.Millisecond); !ok {
			break
		}
	}

	e.StopDiffWatch("run1")
	if _, ok := e.LastFileChange("run1"); ok {
		t.Error("LastFileChange after StopDiffWatch should report false")
	}
	if err := os.WriteFile(filepath.Join(checkout, "post-stop.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if extra, more := nextEvent(t, diffs, 600*time.Millisecond); more {
		t.Fatalf("run.diff after StopDiffWatch: %+v", extra.Payload)
	}
}

// gitcFail runs the client-side git CLI in dir expecting failure; returns
// combined output.
func gitcFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{
		"-c", "protocol.ext.allow=always",
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
	}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		bridgeEnv+"=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git %s unexpectedly succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestUploadPackReturnsOnCtxCancel(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")

	// A hostile or stalled client holds the SSH channel open without ever
	// sending EOF; ctx cancellation is the server's only teardown lever and
	// must unwind the in-flight call.
	ctx, cancel := context.WithCancel(t.Context())
	stdinR, stdinW := io.Pipe()
	defer func() { _ = stdinW.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.UploadPack(ctx, "ws1", stdinR, io.Discard, io.Discard)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * packWaitDelay):
		t.Fatal("UploadPack did not return after ctx cancellation with stdin held open")
	}
}

func TestReceivePackDeniesBranchDeletion(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	if _, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "artifact"); err != nil {
		t.Fatal(err)
	}
	tip, err := e.PublishRunBranch(ctx, "run1")
	if err != nil {
		t.Fatal(err)
	}
	branch := "aether/run-artifact-run1"

	dst := t.TempDir()
	gitc(t, dst, "clone", url("ws1"), "c")
	cl := filepath.Join(dst, "c")
	out := gitcFail(t, cl, "push", url("ws1"), ":refs/heads/"+branch)
	if !strings.Contains(out, "deletion") && !strings.Contains(out, "deny") {
		t.Errorf("deletion push failed for the wrong reason:\n%s", out)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+branch); got != tip {
		t.Fatalf("run branch tip = %s after deletion attempt, want %s", got, tip)
	}
	gitcFail(t, cl, "push", url("ws1"), ":refs/heads/main")
	if got := bareRevParse(t, e, "ws1", "refs/heads/main"); len(got) != 40 {
		t.Fatal("main deleted through ReceivePack")
	}
}

func TestDiffStatsHostilePaths(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "hostile")
	if err != nil {
		t.Fatal(err)
	}
	base, err := e.git(ctx, checkout, "config", cfgBase)
	if err != nil {
		t.Fatal(err)
	}

	// Untracked non-ASCII path: must come back verbatim, not C-quoted.
	if err := os.WriteFile(filepath.Join(checkout, "日本語.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Rename of a tracked file: real paths, never "old => new".
	if _, err := e.git(ctx, checkout, "mv", "file.txt", "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	// Symlink to a FIFO: countLines must not follow it and block forever.
	fifo := filepath.Join(checkout, "pipe")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if err := os.Symlink("pipe", filepath.Join(checkout, "pipelink")); err != nil {
		t.Fatal(err)
	}

	statCh := make(chan []events.FileDiffStat, 1)
	errCh := make(chan error, 1)
	go func() {
		files, statErr := e.diffStats(ctx, checkout, base)
		if statErr != nil {
			errCh <- statErr
			return
		}
		statCh <- files
	}()
	var files []events.FileDiffStat
	select {
	case files = <-statCh:
	case statErr := <-errCh:
		t.Fatalf("diffStats: %v", statErr)
	case <-time.After(10 * time.Second):
		t.Fatal("diffStats hung on symlink to FIFO")
	}

	got := map[string]events.FileDiffStat{}
	for _, f := range files {
		got[f.Path] = f
	}
	if got["日本語.txt"] != (events.FileDiffStat{Path: "日本語.txt", Additions: 2}) {
		t.Errorf("non-ASCII untracked stat = %+v (files %+v)", got["日本語.txt"], files)
	}
	if got["file.txt"] != (events.FileDiffStat{Path: "file.txt", Deletions: 2}) {
		t.Errorf("rename old-path stat = %+v", got["file.txt"])
	}
	if got["renamed.txt"] != (events.FileDiffStat{Path: "renamed.txt", Additions: 2}) {
		t.Errorf("rename new-path stat = %+v", got["renamed.txt"])
	}
	for path := range got {
		if strings.Contains(path, "=>") || strings.Contains(path, `"`) {
			t.Errorf("mangled path in stat set: %q", path)
		}
	}
	if got["pipelink"] != (events.FileDiffStat{Path: "pipelink", Additions: 1}) {
		t.Errorf("symlink stat = %+v", got["pipelink"])
	}
}

func TestReceivePackPublishesGitBranch(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	_, branch, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "pushed from client")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartDiffWatch(ctx, "ws1", "run1"); err != nil {
		t.Fatal(err)
	}
	e.StopDiffWatch("run1") // registry survives; git.branch stays scoped

	branches := subscribeTypes(t, bus, events.TypeGitBranch)

	// A client pushes directly to the run branch through the transport.
	dst := t.TempDir()
	gitc(t, dst, "clone", url("ws1"), "c")
	cl := filepath.Join(dst, "c")
	if err := os.WriteFile(filepath.Join(cl, "client.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, cl, "add", "-A")
	gitc(t, cl, "commit", "-m", "client work")
	gitc(t, cl, "push", url("ws1"), "HEAD:refs/heads/"+branch)
	want := gitc(t, cl, "rev-parse", "HEAD")

	ev, ok := nextEvent(t, branches, 5*time.Second)
	if !ok {
		t.Fatal("no git.branch event after client push to run branch")
	}
	pl := ev.Payload.(events.GitBranchPayload)
	if pl.Branch != branch || pl.Commit != want || pl.WorkspaceID != "ws1" {
		t.Fatalf("git.branch payload = %+v, want %s@%s", pl, branch, want)
	}
	if ev.WorkspaceID != "ws1" || ev.RunID != "run1" {
		t.Fatalf("git.branch scope = %s/%s", ev.WorkspaceID, ev.RunID)
	}

	// A push to a non-run branch publishes nothing.
	gitc(t, cl, "push", url("ws1"), "HEAD:refs/heads/feature")
	if extra, more := nextEvent(t, branches, 600*time.Millisecond); more {
		t.Fatalf("unexpected git.branch for non-run branch: %+v", extra.Payload)
	}
}

// publishRun creates a run checkout with one commit and publishes its
// branch, returning the branch name and its published tip.
func publishRun(t *testing.T, e *Engine, ws domain.WorkspaceID, run domain.RunID, task string) (branch, tip string) {
	t.Helper()
	ctx := t.Context()
	checkout, branch, err := e.CreateRunCheckout(ctx, ws, run, "main", task)
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "work.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CommitAll(ctx, run, "wip"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	tip, err = e.PublishRunBranch(ctx, run)
	if err != nil {
		t.Fatalf("PublishRunBranch: %v", err)
	}
	return branch, tip
}

func TestReceivePackRejectsRunBranchForcePush(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")

	branch, tip := publishRun(t, e, "ws1", "run1", "artifact")

	// Diverge from the published tip and force-push: must be rejected with
	// a message naming the protected ref, leaving the tip untouched.
	dst := t.TempDir()
	gitc(t, dst, "clone", "--branch", branch, url("ws1"), "c")
	cl := filepath.Join(dst, "c")
	gitc(t, cl, "reset", "--hard", "HEAD~1")
	gitc(t, cl, "commit", "--allow-empty", "-m", "divergent")
	out := gitcFail(t, cl, "push", "--force", url("ws1"), branch)
	if !strings.Contains(out, "refs/heads/"+branch) || !strings.Contains(out, "owned by the Aether server") {
		t.Errorf("force-push rejection message unreadable:\n%s", out)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+branch); got != tip {
		t.Fatalf("run branch tip = %s after rejected force-push, want %s", got, tip)
	}

	// A fast-forward client push to the same run branch is still allowed.
	gitc(t, cl, "reset", "--hard", tip)
	if err := os.WriteFile(filepath.Join(cl, "more.txt"), []byte("ff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, cl, "add", "-A")
	gitc(t, cl, "commit", "-m", "fast-forward")
	gitc(t, cl, "push", url("ws1"), branch)
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+branch); got != gitc(t, cl, "rev-parse", "HEAD") {
		t.Fatal("fast-forward push to run branch did not land")
	}
}

func TestReceivePackAllowsFeatureBranchForcePush(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")

	dst := t.TempDir()
	gitc(t, dst, "clone", url("ws1"), "c")
	cl := filepath.Join(dst, "c")
	if err := os.WriteFile(filepath.Join(cl, "feat.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, cl, "add", "-A")
	gitc(t, cl, "commit", "-m", "feature v1")
	gitc(t, cl, "push", url("ws1"), "HEAD:refs/heads/feature")

	// Rewrite history and force-push: normal git-remote semantics stay.
	gitc(t, cl, "reset", "--hard", "HEAD~1")
	gitc(t, cl, "commit", "--allow-empty", "-m", "feature v2")
	want := gitc(t, cl, "rev-parse", "HEAD")
	gitc(t, cl, "push", "--force", url("ws1"), "HEAD:refs/heads/feature")
	if got := bareRevParse(t, e, "ws1", "refs/heads/feature"); got != want {
		t.Fatalf("feature tip = %s after force-push, want %s", got, want)
	}

	// Deletion is still refused even for normal branches.
	gitcFail(t, cl, "push", url("ws1"), ":refs/heads/feature")
	if got := bareRevParse(t, e, "ws1", "refs/heads/feature"); got != want {
		t.Fatal("feature branch deleted through ReceivePack")
	}
}

func TestServerRewriteKeepsOldTipInReflog(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	branch, oldTip := publishRun(t, e, "ws1", "run1", "artifact")

	// Server-side history rewrite: reset the checkout behind the published
	// tip, commit different content, publish again. PublishRunBranch's
	// forced fetch must succeed (update hooks fire on receive-pack only)
	// and the overwritten tip must stay findable in the bare repo reflog.
	checkout, err := e.existingCheckoutPath("run1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "reset", "--hard", "HEAD~1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "rewritten.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newTip, err := e.CommitAll(ctx, "run1", "rewritten")
	if err != nil {
		t.Fatal(err)
	}
	published, err := e.PublishRunBranch(ctx, "run1")
	if err != nil {
		t.Fatalf("server-side rewrite blocked: %v", err)
	}
	if published != newTip || published == oldTip {
		t.Fatalf("published = %s, want rewrite %s (old %s)", published, newTip, oldTip)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+branch+"@{1}"); got != oldTip {
		t.Fatalf("reflog @{1} = %s, want overwritten tip %s", got, oldTip)
	}
}

func TestInitWorkspaceRepoIdempotentOnExistingRepo(t *testing.T) {
	e := newTestEngine(t, nil)
	ctx := t.Context()
	repo, err := e.InitWorkspaceRepo(ctx, "ws1")
	if err != nil {
		t.Fatalf("InitWorkspaceRepo: %v", err)
	}

	// Strip the settings to simulate a repo created before .
	if _, err := e.git(ctx, repo, "config", "--unset", "core.logAllRefUpdates"); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repo, "hooks", "update")
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}

	// First touch of an existing repo restores both.
	if _, err := e.InitWorkspaceRepo(ctx, "ws1"); err != nil {
		t.Fatalf("InitWorkspaceRepo on existing repo: %v", err)
	}
	if v, _ := e.git(ctx, repo, "config", "--type=bool", "core.logAllRefUpdates"); v != "true" {
		t.Errorf("core.logAllRefUpdates = %q, want true", v)
	}
	fi, err := os.Stat(hook)
	if err != nil {
		t.Fatalf("update hook not restored: %v", err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("update hook not executable: %v", fi.Mode())
	}
	data, err := os.ReadFile(hook)
	if err != nil || string(data) != updateHook {
		t.Errorf("update hook content mismatch (err %v)", err)
	}

	// Repeat touches settle into a no-op that leaves everything in place.
	if _, err := e.InitWorkspaceRepo(ctx, "ws1"); err != nil {
		t.Fatalf("third InitWorkspaceRepo: %v", err)
	}
	if data2, _ := os.ReadFile(hook); string(data2) != updateHook {
		t.Error("update hook changed on repeat init")
	}
}
func TestDiffWatchPublishesCommitWithoutTreeEvent(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run-commit-only", "main", "watch me")
	if err != nil {
		t.Fatal(err)
	}
	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	branches := subscribeTypes(t, bus, events.TypeGitBranch)
	if err := e.StartDiffWatch(ctx, "ws1", "run-commit-only"); err != nil {
		t.Fatalf("StartDiffWatch: %v", err)
	}

	if err := os.WriteFile(filepath.Join(checkout, "commit-only.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := nextEvent(t, diffs, 5*time.Second); !ok {
		t.Fatal("no run.diff event after write")
	}

	if _, err := e.git(ctx, checkout, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "-c", "user.name=Agent", "-c", "user.email=a@x", "commit", "-m", "commit-only"); err != nil {
		t.Fatal(err)
	}

	ev, ok := nextEvent(t, branches, 5*time.Second)
	if !ok {
		t.Fatal("no git.branch event after commit without tree edit")
	}
	payload, ok := ev.Payload.(events.GitBranchPayload)
	if !ok {
		t.Fatalf("payload type = %T, want events.GitBranchPayload", ev.Payload)
	}
	head, err := e.git(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if payload.Commit != head {
		t.Fatalf("published commit = %s, want %s", payload.Commit, head)
	}
}
