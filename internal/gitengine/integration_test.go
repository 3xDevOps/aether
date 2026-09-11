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
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

	checkout, branch, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "Fix the Auth bug!", "")
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
	if _, _, dupErr := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "x", ""); dupErr == nil {
		t.Error("duplicate CreateRunCheckout should fail")
	}
	if _, _, baseErr := e.CreateRunCheckout(ctx, "ws1", "run2", "no-such-branch", "x", ""); baseErr == nil {
		t.Error("CreateRunCheckout from unborn base should fail")
	}

	// Clean tree: CommitAll is a no-op.
	if noop, noopErr := e.CommitAll(ctx, "run1", "aether: noop", domain.GitIdentity{}, nil); noopErr != nil || noop != "" {
		t.Fatalf("clean CommitAll = (%q, %v), want (\"\", nil)", noop, noopErr)
	}

	// Dirty tree: wip commit authored as the run owner, committed by Aether.
	if writeErr := os.WriteFile(filepath.Join(checkout, "new.txt"), []byte("hi\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	owner := domain.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}
	commit, err := e.CommitAll(ctx, "run1",
		"wip: fix the auth bug\n\nCo-authored-by: Bob <bob@example.com>", owner, nil)
	if err != nil || len(commit) != 40 {
		t.Fatalf("CommitAll = (%q, %v)", commit, err)
	}
	// The run owner is the author; Aether stays the committer, and the
	// trailer rides in the message the scheduler assembled.
	if author, _ := e.git(ctx, checkout, "log", "-1", "--format=%an <%ae>"); author != owner.String() {
		t.Errorf("commit author = %q, want %q", author, owner)
	}
	if committer, _ := e.git(ctx, checkout, "log", "-1", "--format=%cn <%ce>"); committer != "Aether <aether@localhost>" {
		t.Errorf("commit committer = %q", committer)
	}
	if body, _ := e.git(ctx, checkout, "log", "-1", "--format=%(trailers:key=Co-authored-by,valueonly)"); body != "Bob <bob@example.com>" {
		t.Errorf("commit co-author trailer = %q", body)
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
	_, firstBranch, err := e.CreateRunCheckout(ctx, "ws1", first, "main", "Fix the Auth bug!", "")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if firstBranch != "aether/run-fix-the-auth-bug-nq0jf3" {
		t.Fatalf("first branch = %q, want the short-id form", firstBranch)
	}

	_, branch, err := e.CreateRunCheckout(ctx, "ws1", colliding, "main", "Fix the Auth bug!", "")
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
	_, _, err := e.CreateRunCheckout(ctx, "ws1", run, "main", "Fix the Auth bug!", "")
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
	_, branch, err := e.CreateRunCheckout(ctx, "ws1", run, "main", "Fix the Auth bug!", "")
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

	checkout, branch, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "attack", "")
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
	hostile, err := e.CommitAll(ctx, "run1", "hostile", domain.GitIdentity{}, nil)
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
			checkout, _, err := e.CreateRunCheckout(ctx, "ws1", run, "main", fmt.Sprintf("task %d", i), "")
			if err != nil {
				errs <- fmt.Errorf("%s create: %w", run, err)
				return
			}
			if err := os.WriteFile(filepath.Join(checkout, fmt.Sprintf("f%d.txt", i)), []byte("x\n"), 0o644); err != nil {
				errs <- err
				return
			}
			if _, err := e.CommitAll(ctx, run, "aether: work", domain.GitIdentity{}, nil); err != nil {
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

// TestDiffWatchPrunesGitIgnoredTrees is a real-git reproduction for watch
// pressure. The generated tree is ignored, but the checkout also contains a
// tracked file under an ignored directory and an existing path re-included by
// a negated rule. Churn in the ignored subtree must not postpone the
// observable snapshot containing either visible path.
func TestDiffWatchPrunesGitIgnoredTrees(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	e.cfg.QuietPeriod = 20 * time.Millisecond
	e.cfg.MinInterval = 20 * time.Millisecond
	e.cfg.MaxInterval = 10 * time.Minute
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()
	diffs := subscribeTypes(t, bus, events.TypeRunDiff)

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "ignore pressure", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".gitignore"), []byte("generated/*\ntracked-cache/\n!generated/keep.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trackedDir := filepath.Join(checkout, "tracked-cache")
	noiseDir := filepath.Join(checkout, "generated", "noise")
	if err := os.MkdirAll(trackedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(noiseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(trackedDir, "checked-in.txt")
	keep := filepath.Join(checkout, "generated", "keep.txt")
	noise := filepath.Join(noiseDir, "output.txt")
	if err := os.WriteFile(tracked, []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("included\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noise, []byte("noise\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "add", ".gitignore"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "add", "-f", "tracked-cache/checked-in.txt"); err != nil {
		t.Fatal(err)
	}
	if err := e.StartDiffWatch(ctx, "ws1", "run1"); err != nil {
		t.Fatalf("StartDiffWatch: %v", err)
	}
	stopChurn := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopChurn) }) }
	churnDone := make(chan struct{})
	go func() {
		defer close(churnDone)
		for {
			select {
			case <-stopChurn:
				return
			default:
			}
			_ = os.WriteFile(noise, []byte("ignored churn\n"), 0o644)
		}
	}()
	t.Cleanup(func() {
		stop()
		<-churnDone
	})
	if err := os.WriteFile(tracked, []byte("tracked changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("included changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ev, ok := nextEvent(t, diffs, 2*time.Second)
	if !ok {
		t.Fatal("ignored subtree churn delayed the tracked snapshot")
	}
	stop()
	<-churnDone
	payload := ev.Payload.(events.RunDiffPayload)
	seen := make(map[string]bool, len(payload.Files))
	for _, file := range payload.Files {
		seen[file.Path] = true
	}
	if !seen["tracked-cache/checked-in.txt"] {
		t.Fatalf("tracked file under ignored directory was not watched: %+v", payload.Files)
	}
	if !seen["generated/keep.txt"] {
		t.Fatalf("negated path under ignored tree was not watched: %+v", payload.Files)
	}
}

// TestDiffWatchReconcilesLiveIgnoreRules exercises the transition from an
// ignored directory to a visible one while a watch is already running. The
// directory is present at startup, so discovering the file relies on the
// ignore-file invalidation rather than a fresh-directory walk.
func TestDiffWatchReconcilesLiveIgnoreRules(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	e.cfg.QuietPeriod = 20 * time.Millisecond
	e.cfg.MinInterval = 20 * time.Millisecond
	e.cfg.MaxInterval = 10 * time.Minute
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run-live-ignore", "main", "live ignore", "")
	if err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(checkout, ".gitignore")
	liveDir := filepath.Join(checkout, "live")
	if err := os.WriteFile(ignore, []byte("live/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "before.txt"), []byte("ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "add", ".gitignore"); err != nil {
		t.Fatal(err)
	}

	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	if err := e.StartDiffWatch(ctx, "ws1", "run-live-ignore"); err != nil {
		t.Fatalf("StartDiffWatch: %v", err)
	}

	if err := os.WriteFile(ignore, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	visible := filepath.Join(liveDir, "after.txt")
	if err := os.WriteFile(visible, []byte("now visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, ok := nextEvent(t, diffs, 5*time.Second)
	if !ok {
		t.Fatal("no run.diff after removing a live ignore rule")
	}
	payload := ev.Payload.(events.RunDiffPayload)
	seen := make(map[string]bool, len(payload.Files))
	for _, file := range payload.Files {
		seen[file.Path] = true
	}
	if !seen["live/after.txt"] {
		t.Fatalf("newly unignored file was not observed: %+v", payload.Files)
	}
}

// TestDiffWatchIgnoresDirectoryCreatedAfterStart verifies that a generated
// tree created after startup is classified by a coalesced Git refresh before
// any descendant walk. Once refreshed, churn in that tree is neither a
// visible change nor a LastFileChange event.
func TestDiffWatchIgnoresDirectoryCreatedAfterStart(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	e.cfg.QuietPeriod = 20 * time.Millisecond
	e.cfg.MinInterval = 20 * time.Millisecond
	e.cfg.MaxInterval = 10 * time.Minute
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run-created-ignore", "main", "created ignore", "")
	if err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(checkout, ".gitignore")
	if err := os.WriteFile(ignore, []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "add", ".gitignore"); err != nil {
		t.Fatal(err)
	}
	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	if err := e.StartDiffWatch(ctx, "ws1", "run-created-ignore"); err != nil {
		t.Fatalf("StartDiffWatch: %v", err)
	}

	generated := filepath.Join(checkout, "node_modules", "pkg", "output.txt")
	if err := os.MkdirAll(filepath.Dir(generated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	visible := filepath.Join(checkout, "visible.txt")
	if err := os.WriteFile(visible, []byte("visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, ok := nextEvent(t, diffs, 5*time.Second)
	if !ok {
		t.Fatal("no run.diff after creating a visible file alongside ignored tree")
	}
	payload := ev.Payload.(events.RunDiffPayload)
	seenVisible := false
	for _, file := range payload.Files {
		if file.Path == "visible.txt" {
			seenVisible = true
			break
		}
	}
	if !seenVisible {
		t.Fatalf("visible file missing from diff: %+v", payload.Files)
	}
	last, changed := e.LastFileChange("run-created-ignore")
	if !changed {
		t.Fatal("LastFileChange was not set by visible file")
	}

	if err := os.WriteFile(generated, []byte("generated churn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if extra, more := nextEvent(t, diffs, 500*time.Millisecond); more {
		t.Fatalf("ignored tree churn produced run.diff: %+v", extra.Payload)
	}
	if got, _ := e.LastFileChange("run-created-ignore"); !got.Equal(last) {
		t.Fatalf("ignored tree churn updated LastFileChange: before %v, after %v", last, got)
	}
}

// inotifyWatchCount reports kernel watches held by this process. It is a
// resource observation rather than an assertion about diffWatch's private
// directory set, and is only available on Linux.
func inotifyWatchCount() (int, bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil || !strings.Contains(target, "inotify") {
			continue
		}
		info, err := os.ReadFile(filepath.Join("/proc/self/fdinfo", entry.Name()))
		if err != nil {
			return 0, false
		}
		for line := range strings.SplitSeq(string(info), "\n") {
			if strings.HasPrefix(line, "inotify wd:") {
				count++
			}
		}
	}
	return count, true
}

// TestDiffWatchPrunesExistingTreeAfterIgnoreUpdate observes that a tree
// watched while visible relinquishes its descendant kernel watches when a
// later ignore rule prunes it. Git metadata watches remain available for
// subsequent ignore/index changes.
func TestDiffWatchPrunesExistingTreeAfterIgnoreUpdate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux /proc inotify accounting")
	}
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	e.cfg.QuietPeriod = 20 * time.Millisecond
	e.cfg.MinInterval = 20 * time.Millisecond
	e.cfg.MaxInterval = 10 * time.Minute
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run-prune-existing", "main", "prune existing", "")
	if err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(checkout, ".gitignore")
	if err := os.WriteFile(ignore, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	generatedRoot := filepath.Join(checkout, "generated")
	const descendantCount = 256
	for i := range descendantCount {
		dir := filepath.Join(generatedRoot, fmt.Sprintf("dir-%03d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "output.txt"), []byte("generated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.git(ctx, checkout, "add", ".gitignore"); err != nil {
		t.Fatal(err)
	}
	before, found := inotifyWatchCount()
	if !found {
		t.Skip("kernel does not expose inotify fd accounting")
	}
	if err := e.StartDiffWatch(ctx, "ws1", "run-prune-existing"); err != nil {
		t.Fatalf("StartDiffWatch: %v", err)
	}
	expanded, found := inotifyWatchCount()
	if !found || expanded-before < descendantCount {
		t.Fatalf("visible tree did not consume expected kernel watches: before=%d expanded=%d", before, expanded)
	}

	if err := os.WriteFile(ignore, []byte("generated/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "add", ".gitignore"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if got, ok := inotifyWatchCount(); ok && got <= expanded-descendantCount+2 {
			break
		}
		if !time.Now().Before(deadline) {
			got, _ := inotifyWatchCount()
			t.Fatalf("ignored tree retained kernel watches: before=%d expanded=%d final=%d", before, expanded, got)
		}
		<-ticker.C
	}
}

// TestDiffWatchReconcilesTrackedAndNegatedPaths covers both ways a path can
// become visible without creating a directory: force-adding an existing
// ignored file updates the index, while a new file matching a pre-existing
// negated rule arrives below an ignored ancestor.
func TestDiffWatchReconcilesTrackedAndNegatedPaths(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	e.cfg.QuietPeriod = 20 * time.Millisecond
	e.cfg.MinInterval = 20 * time.Millisecond
	e.cfg.MaxInterval = 10 * time.Minute
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run-visible-paths", "main", "visible paths", "")
	if err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(checkout, ".gitignore")
	if err := os.WriteFile(ignore, []byte("tracked-cache/\ngenerated/*\n!generated/keep.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trackedDir := filepath.Join(checkout, "tracked-cache")
	generatedDir := filepath.Join(checkout, "generated")
	if err := os.MkdirAll(trackedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(trackedDir, "forced.txt")
	if err := os.WriteFile(tracked, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "add", ".gitignore"); err != nil {
		t.Fatal(err)
	}

	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	if err := e.StartDiffWatch(ctx, "ws1", "run-visible-paths"); err != nil {
		t.Fatalf("StartDiffWatch: %v", err)
	}

	if err := os.WriteFile(tracked, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "add", "-f", "tracked-cache/forced.txt"); err != nil {
		t.Fatal(err)
	}
	ev, ok := nextEvent(t, diffs, 5*time.Second)
	if !ok {
		t.Fatal("no run.diff after force-adding an ignored file")
	}
	payload := ev.Payload.(events.RunDiffPayload)
	seen := make(map[string]bool, len(payload.Files))
	for _, file := range payload.Files {
		seen[file.Path] = true
	}
	if !seen["tracked-cache/forced.txt"] {
		t.Fatalf("force-added ignored file was not observed: %+v", payload.Files)
	}

	negated := filepath.Join(generatedDir, "keep.txt")
	if err := os.WriteFile(negated, []byte("included\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, ok = nextEvent(t, diffs, 5*time.Second)
	if !ok {
		t.Fatal("no run.diff after creating a negated file")
	}
	payload = ev.Payload.(events.RunDiffPayload)
	seen = make(map[string]bool, len(payload.Files))
	for _, file := range payload.Files {
		seen[file.Path] = true
	}
	if !seen["generated/keep.txt"] {
		t.Fatalf("newly negated file was not observed: %+v", payload.Files)
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

	checkout, branch, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "watch me", "")
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
	first := ev.Payload.(events.RunDiffPayload)
	files := first.Files
	if len(files) != 1 || files[0] != (events.FileDiffStat{Path: "notes.txt", Additions: 2}) {
		t.Fatalf("diff files = %+v", files)
	}
	// Every snapshot records a tree, and the first one's parent is the
	// fork-point tree: diffing parent to tree is what this interval changed.
	if !validObjectID(first.Tree) {
		t.Fatalf("first snapshot tree = %q, want an object id", first.Tree)
	}
	forkTree, err := e.git(ctx, checkout, "rev-parse", bareRevParse(t, e, "ws1", "refs/heads/main")+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if first.ParentTree != forkTree {
		t.Errorf("first parent tree = %q, want the fork-point tree %q", first.ParentTree, forkTree)
	}
	if p, err := e.RunPatch(ctx, "run1", PatchRequest{From: first.ParentTree, To: first.Tree}); err != nil {
		t.Errorf("RunPatch over the first interval: %v", err)
	} else if !strings.Contains(p.Text, "+a") {
		t.Errorf("first interval patch is missing the write:\n%s", p.Text)
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
	prevTree := first.Tree
	deadline := time.Now().Add(5 * time.Second)
	for len(got) != 3 && time.Now().Before(deadline) {
		ev, ok = nextEvent(t, diffs, 2*time.Second)
		if !ok {
			break
		}
		payload := ev.Payload.(events.RunDiffPayload)
		// The chain is unbroken: each snapshot's parent is the tree the
		// previous snapshot recorded, so consecutive events bound one
		// interval exactly.
		if payload.ParentTree != prevTree {
			t.Errorf("snapshot parent tree = %q, want the previous snapshot's tree %q", payload.ParentTree, prevTree)
		}
		if !validObjectID(payload.Tree) {
			t.Errorf("snapshot tree = %q, want an object id", payload.Tree)
		}
		prevTree = payload.Tree
		got = map[string]events.FileDiffStat{}
		for _, f := range payload.Files {
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

type blockingPackWriter struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingPackWriter() *blockingPackWriter {
	return &blockingPackWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingPackWriter) Write([]byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.release
	return 0, io.ErrClosedPipe
}

func (w *blockingPackWriter) Close() error {
	w.closeOnce.Do(func() { close(w.release) })
	return nil
}
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("read child pid file: %v", err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("child pid file %s was not ready", path)
		}
		<-ticker.C
	}
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	procPath := filepath.Join("/proc", strconv.Itoa(pid))
	deadline := time.Now().Add(2 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := os.Stat(procPath)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("stat child process %d: %v", pid, err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("child process %d did not exit and get reaped", pid)
		}
		<-ticker.C
	}
}

func TestUploadPackReturnsOnCtxCancelWithBlockedOutputAfterReap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux /proc process lifecycle observation")
	}
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "git.pid")
	gitWrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\" > " + shellQuote(pidFile) +
		"\nexec " + shellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(gitWrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	packConfig := e.cfg
	packConfig.GitPath = gitWrapper
	packEngine, err := New(packConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packEngine.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := newBlockingPackWriter()
	t.Cleanup(func() { _ = out.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Invalid input makes git write a diagnostic, then the writer holds
		// the os/exec output-copy goroutine in Write until cancellation.
		_, _ = packEngine.UploadPack(ctx, "ws1", strings.NewReader("not a pkt-line\n"), io.Discard, out)
	}()
	select {
	case <-out.started:
	case <-time.After(2 * time.Second):
		t.Fatal("git upload-pack did not reach the blocked output writer")
	}
	pid := waitForPIDFile(t, pidFile)
	// /proc disappearance proves Process.Wait reaped the child, not merely
	// that the wrapper wrote a ready marker before exiting.
	waitForProcessGone(t, pid)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * packWaitDelay):
		t.Fatal("UploadPack did not return after cancellation with output blocked")
	}
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

	if _, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "artifact", ""); err != nil {
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

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "hostile", "")
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

	_, branch, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "pushed from client", "")
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
	checkout, branch, err := e.CreateRunCheckout(ctx, ws, run, "main", task, "")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "work.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CommitAll(ctx, run, "wip", domain.GitIdentity{}, nil); err != nil {
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
	newTip, err := e.CommitAll(ctx, "run1", "rewritten", domain.GitIdentity{}, nil)
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

	checkout, branch, err := e.CreateRunCheckout(ctx, "ws1", "run-commit-only", "main", "watch me", "")
	if err != nil {
		t.Fatal(err)
	}
	branches := subscribeTypes(t, bus, events.TypeGitBranch)
	if err := e.StartDiffWatch(ctx, "ws1", "run-commit-only"); err != nil {
		t.Fatalf("StartDiffWatch: %v", err)
	}

	tree, err := e.git(ctx, checkout, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := e.git(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := e.git(ctx, checkout, "-c", "user.name=Agent", "-c", "user.email=a@x", "commit-tree", tree, "-p", parent, "-m", "commit-only")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctx, checkout, "update-ref", "refs/heads/"+branch, commit, parent); err != nil {
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

// A display name is member-supplied and never validated, and git reads the
// first <...> in an author as the address. A name like
// "Eve <attacker@evil.com>" would therefore author every commit of that
// member's runs as an address they do not hold, and GitHub would link it to
// whoever does. The identity falls back to the member id instead, which
// credits nobody - the honest answer.
func TestCommitAllRefusesAForgedDisplayName(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	for i, displayName := range []string{
		"Eve <attacker@evil.com>",
		"Eve\nCo-authored-by: Eve <attacker@evil.com>",
	} {
		run := domain.RunID(fmt.Sprintf("forge%d", i))
		checkout, _, err := e.CreateRunCheckout(ctx, "ws1", run, "main", "forged", "")
		if err != nil {
			t.Fatalf("CreateRunCheckout: %v", err)
		}
		if werr := os.WriteFile(filepath.Join(checkout, "new.txt"), []byte("hi\n"), 0o644); werr != nil {
			t.Fatal(werr)
		}
		id := (&domain.Member{ID: "m_eve", DisplayName: displayName}).GitIdentity()
		if _, cerr := e.CommitAll(ctx, run, "wip: forged\n\n"+id.Trailer(), id, nil); cerr != nil {
			t.Fatalf("CommitAll with display name %q: %v", displayName, cerr)
		}
		if got, _ := e.git(ctx, checkout, "log", "-1", "--format=%ae"); got != "m_eve@aether.local" {
			t.Errorf("display name %q authored the commit as %q", displayName, got)
		}
		if got, _ := e.git(ctx, checkout, "log", "-1", "--format=%an"); got != "m_eve" {
			t.Errorf("display name %q named the author %q", displayName, got)
		}
		trailer, _ := e.git(ctx, checkout, "log", "-1", "--format=%(trailers:key=Co-authored-by,valueonly)")
		if trailer != "m_eve <m_eve@aether.local>" {
			t.Errorf("display name %q produced the trailer %q", displayName, trailer)
		}
	}
}

func TestMirrorLifecycleWithExplicitLocalTransportSeam(t *testing.T) {
	e := newTestEngine(t, nil)
	ctx := t.Context()
	source := filepath.Join(t.TempDir(), "source.git")
	gitc(t, filepath.Dir(source), "init", "--bare", source)
	work := filepath.Join(t.TempDir(), "work")
	gitc(t, filepath.Dir(work), "init", work)
	if err := os.WriteFile(filepath.Join(work, "one.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, work, "add", "-A")
	gitc(t, work, "commit", "-m", "one")
	gitc(t, work, "push", source, "HEAD:refs/heads/main")

	if _, err := e.InitWorkspaceRepo(ctx, "mirror"); err != nil {
		t.Fatal(err)
	}
	e.cfg.MirrorFetch = func(ctx context.Context, repo string, req MirrorRequest, incoming string) error {
		cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--no-tags", "--no-write-fetch-head", req.SourceURL, "+refs/heads/"+req.Branch+":"+incoming)
		return cmd.Run()
	}
	req := MirrorRequest{SourceURL: source, Branch: "main", Generation: 1, Auth: domain.MirrorAuthPublic}
	if _, err := e.ConfigureWorkspaceMirror(ctx, "mirror", req); err != nil {
		t.Fatalf("ConfigureWorkspaceMirror: %v", err)
	}
	first, err := e.RefreshWorkspaceMirror(ctx, "mirror", req)
	if err != nil {
		t.Fatalf("initial RefreshWorkspaceMirror: %v", err)
	}
	if first.Status != domain.MirrorStatusPending || first.CandidateCommit == "" {
		t.Fatalf("initial mirror result = %+v", first)
	}
	adopted, err := e.AdoptWorkspaceMirror(ctx, "mirror", int64(1))
	if err != nil {
		t.Fatalf("AdoptWorkspaceMirror: %v", err)
	}
	if adopted.BaseCommit == "" || adopted.BaseCommit != adopted.AcceptedCommit {
		t.Fatalf("adopted mirror result = %+v", adopted)
	}
	old := adopted.BaseCommit

	if err := os.WriteFile(filepath.Join(work, "two.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, work, "add", "-A")
	gitc(t, work, "commit", "-m", "two")
	gitc(t, work, "push", source, "HEAD:refs/heads/main")
	ff, err := e.RefreshWorkspaceMirror(ctx, "mirror", req)
	if err != nil || ff.BaseCommit == old || ff.Status != domain.MirrorStatusReady {
		t.Fatalf("fast-forward refresh = %+v, err %v", ff, err)
	}
	newTip := ff.BaseCommit

	gitc(t, work, "reset", "--hard", "HEAD~1")
	gitc(t, work, "commit", "--allow-empty", "-m", "rewrite")
	gitc(t, work, "push", "--force", source, "HEAD:refs/heads/main")
	rewritten, err := e.RefreshWorkspaceMirror(ctx, "mirror", req)
	var me *MirrorError
	if !errors.As(err, &me) || me.Kind != MirrorErrorRewritten {
		t.Fatalf("rewrite refresh err = %v, want MirrorErrorRewritten", err)
	}
	if rewritten.BaseCommit != newTip || rewritten.CandidateCommit == rewritten.BaseCommit {
		t.Fatalf("rewrite refresh changed accepted/base: %+v", rewritten)
	}

	if _, err := e.git(ctx, mustRepo(t, e, "mirror"), "update-ref", "refs/heads/main", old); err != nil {
		t.Fatal(err)
	}
	diverged, err := e.RefreshWorkspaceMirror(ctx, "mirror", req)
	if !errors.As(err, &me) || me.Kind != MirrorErrorDiverged {
		t.Fatalf("base divergence err = %v, want MirrorErrorDiverged (result %+v)", err, diverged)
	}
	if got := bareRevParse(t, e, "mirror", "refs/heads/main"); got != old {
		t.Fatalf("diverged base = %s, want %s", got, old)
	}

	sourceB := filepath.Join(t.TempDir(), "source-b.git")
	gitc(t, filepath.Dir(sourceB), "init", "--bare", sourceB)
	gitc(t, work, "push", sourceB, "HEAD:refs/heads/main")
	reqB := MirrorRequest{SourceURL: sourceB, Branch: "main", Generation: 2, Auth: domain.MirrorAuthPublic}
	if _, err := e.ConfigureWorkspaceMirror(ctx, "mirror", reqB); err != nil {
		t.Fatalf("reconfigure mirror: %v", err)
	}
	repo := mustRepo(t, e, "mirror")
	if got := readRefBestEffort(ctx, e, repo, "refs/aether/mirror/1/accepted"); got != "" {
		t.Fatalf("old accepted ref survived reconfigure: %s", got)
	}
	if got := readRefBestEffort(ctx, e, repo, "refs/aether/mirror/1/candidate"); got != "" {
		t.Fatalf("old candidate ref survived reconfigure: %s", got)
	}
	var staleErr *MirrorError
	if _, err := e.AdoptWorkspaceMirror(ctx, "mirror", 1); !errors.As(err, &staleErr) || staleErr.Kind != MirrorErrorNotConfigured {
		t.Fatalf("stale generation adoption error = %v, want not-configured", err)
	}
	if err := e.DisableWorkspaceMirror(ctx, "mirror"); err != nil {
		t.Fatalf("disable mirror: %v", err)
	}
	if generation, err := e.MirrorGeneration(ctx, "mirror"); err != nil || generation != 2 {
		t.Fatalf("durable generation after disable = %d, %v; want 2", generation, err)
	}
	reqB.Generation = 3
	if _, err := e.ConfigureWorkspaceMirror(ctx, "mirror", reqB); err != nil {
		t.Fatalf("configure after disable: %v", err)
	}
	if generation, err := e.MirrorGeneration(ctx, "mirror"); err != nil || generation != 3 {
		t.Fatalf("durable generation after reconfigure = %d, %v; want 3", generation, err)
	}
	if _, err := e.AdoptWorkspaceMirror(ctx, "mirror", 2); !errors.As(err, &staleErr) || staleErr.Kind != MirrorErrorNotConfigured {
		t.Fatalf("disabled generation adoption error = %v, want not-configured", err)
	}
}

func mustRepo(t *testing.T, e *Engine, ws domain.WorkspaceID) string {
	t.Helper()
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestMirrorProtectedRefsAndNormalReads(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	e.cfg.MirrorFetch = func(context.Context, string, MirrorRequest, string) error { return nil }
	req := MirrorRequest{SourceURL: "https://upstream.invalid/repo.git", Branch: "main", Generation: 7, Auth: domain.MirrorAuthPublic}
	if _, err := e.ConfigureWorkspaceMirror(t.Context(), "ws1", req); err != nil {
		t.Fatal(err)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")
	if _, err := e.git(t.Context(), mustRepo(t, e, "ws1"), "update-ref", "refs/aether/mirror/7/candidate", base); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	gitc(t, dst, "clone", url("ws1"), "clone")
	cl := filepath.Join(dst, "clone")
	if err := os.WriteFile(filepath.Join(cl, "blocked.txt"), []byte("blocked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, cl, "add", "-A")
	gitc(t, cl, "commit", "-m", "blocked")
	if out := gitcFail(t, cl, "push", url("ws1"), "HEAD:refs/aether/mirror/7/candidate"); !strings.Contains(out, "protected ref") && !strings.Contains(out, "hidden ref") {
		t.Fatalf("hidden mirror ref was not rejected by receive-pack: %q", out)
	}
	if out := gitcFail(t, cl, "push", url("ws1"), "HEAD:refs/heads/main"); !strings.Contains(out, "upstream") {
		t.Fatalf("mirrored-base rejection lacks upstream guidance: %q", out)
	}
	if got := gitc(t, cl, "ls-remote", url("ws1"), "refs/aether/mirror/7/candidate"); got != "" {
		t.Fatalf("hidden ref advertised as %q", got)
	}
	if err := e.DisableWorkspaceMirror(t.Context(), "ws1"); err != nil {
		t.Fatalf("DisableWorkspaceMirror: %v", err)
	}
	gitc(t, cl, "push", url("ws1"), "HEAD:refs/heads/main")
	if got := gitc(t, cl, "ls-remote", url("ws1"), "refs/heads/main"); got == "" {
		t.Fatalf("normal mirrored base not readable: %q", got)
	}
}
