package helper

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"google.golang.org/grpc"
)

const (
	testWorkspace = domain.WorkspaceID("ws1")
	testRun       = domain.RunID("ses1")
)

// gitPair is a server engine whose session checkout lives in a git service
// of this process, as one in a VM would.
type gitPair struct {
	host     *gitengine.Engine
	git      *gitService
	repo     string
	base     string
	branch   string
	checkout string
	diffs    <-chan events.Event
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@localhost"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newGitPair serves server through a helper socket; nil serves the real
// git service.
func newGitPair(t *testing.T, server func(*gitService) guestpb.GitServer) *gitPair {
	t.Helper()
	ctx := testContext(t)
	root := t.TempDir()
	p := &gitPair{repo: filepath.Join(root, "repos", string(testWorkspace)+".git")}
	runGit(t, root, "init", "--quiet", "--bare", "--initial-branch=main", p.repo)
	seed := filepath.Join(root, "seed")
	runGit(t, root, "clone", "--quiet", p.repo, seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "README")
	runGit(t, seed, "commit", "--quiet", "-m", "first")
	runGit(t, seed, "push", "--quiet", "origin", "HEAD:main")
	p.base = runGit(t, seed, "rev-parse", "HEAD")

	guestDir := filepath.Join(root, "guest")
	git, err := newGitService(gitengine.Config{
		ReposDir: filepath.Join(guestDir, "repos"), CheckoutsDir: guestDir,
		QuietPeriod: 50 * time.Millisecond, MinInterval: 100 * time.Millisecond, MaxInterval: time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.git = git
	t.Cleanup(func() { _ = git.engine.Close() })
	socket := filepath.Join(t.TempDir(), "vsock.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	if server == nil {
		guestpb.RegisterGitServer(srv, git)
	} else {
		guestpb.RegisterGitServer(srv, server(git))
	}
	go func() { _ = srv.Serve(hybridVsock{listener}) }()
	t.Cleanup(srv.Stop)
	client, err := guest.DialGit(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	sub, err := bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunDiff}}})
	if err != nil {
		t.Fatal(err)
	}
	p.diffs = sub.Events()
	p.host, err = gitengine.New(gitengine.Config{
		ReposDir: filepath.Join(root, "repos"), CheckoutsDir: filepath.Join(root, "checkouts"), Bus: bus,
		VM: &gitengine.VM{Guest: func(context.Context, domain.RunID) (*guest.Git, error) { return client, nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.host.Close() })
	if _, p.branch, err = p.host.CreateRunCheckoutAt(ctx, testWorkspace, testRun, p.base, "main", "a task", ""); err != nil {
		t.Fatal(err)
	}
	// What the checkout's prepared disk holds.
	p.checkout = filepath.Join(guestDir, string(testRun))
	runGit(t, root, "clone", "--quiet", "--no-hardlinks", "--no-checkout", p.repo, p.checkout)
	runGit(t, p.checkout, "checkout", "--quiet", "-b", gitengine.PreparedBranch, p.base)
	if err := git.setup(ctx, []*guestpb.Checkout{{
		Key: string(testRun), Workspace: string(testWorkspace), Branch: p.branch, Base: p.base, BaseBranch: "main",
		Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid()),
	}}); err != nil {
		t.Fatal(err)
	}
	return p
}

// nextDiff waits for a run.diff event that ok accepts.
func (p *gitPair) nextDiff(t *testing.T, ok func(events.RunDiffPayload) bool) events.RunDiffPayload {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case ev := <-p.diffs:
			if payload, isDiff := ev.Payload.(events.RunDiffPayload); isDiff && ok(payload) {
				return payload
			}
		case <-timeout:
			t.Fatal("no matching run.diff event")
		}
	}
}

func hasFile(name string) func(events.RunDiffPayload) bool {
	return func(p events.RunDiffPayload) bool {
		for _, f := range p.Files {
			if f.Path == name && p.Tree != "" {
				return true
			}
		}
		return false
	}
}

func TestGitServiceRecordsSnapshotsOnTheServer(t *testing.T) {
	p := newGitPair(t, nil)
	ctx := testContext(t)
	if head := runGit(t, p.checkout, "symbolic-ref", "HEAD"); head != "refs/heads/"+p.branch {
		t.Fatalf("checkout HEAD is %s, want the run branch %s", head, p.branch)
	}
	if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.checkout, "new.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := p.nextDiff(t, hasFile("new.txt"))
	store := filepath.Join(filepath.Dir(p.repo), "..", "checkouts", string(testRun)+".diffsnap")
	if kind := runGit(t, store, "cat-file", "-t", diff.Tree); kind != "tree" {
		t.Fatalf("server store holds %s as a %s", diff.Tree, kind)
	}
	if diff.ParentTree != runGit(t, p.repo, "rev-parse", p.base+"^{tree}") {
		t.Fatalf("first interval starts at %s, want the fork point's tree", diff.ParentTree)
	}

	patch, err := p.host.RunPatch(ctx, testRun, gitengine.PatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch.Text, "+++ b/new.txt") || patch.Base != p.base {
		t.Fatalf("live patch = %+v", patch)
	}

	runGit(t, p.checkout, "add", "new.txt")
	runGit(t, p.checkout, "commit", "--quiet", "-m", "add new")
	head := runGit(t, p.checkout, "rev-parse", "HEAD")
	deadline := time.Now().Add(15 * time.Second)
	for runGitQuiet(p.repo, "rev-parse", "refs/heads/"+p.branch) != head {
		if time.Now().After(deadline) {
			t.Fatalf("run branch never moved to the checkout's commit %s", head)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err = os.WriteFile(filepath.Join(p.checkout, "last.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.host.StopDiffWatch(testRun)
	if got := runGitQuiet(store, "ls-tree", "--name-only", "refs/aether/published", "last.txt"); got != "last.txt" {
		t.Fatalf("the last recorded snapshot lacks the file written just before the stop: %q", got)
	}
	if _, ok := p.host.LastFileChange(testRun); ok {
		t.Fatal("a stopped watch still reports file changes")
	}

	// A server that runs no VMs, as after switching back to Docker sessions,
	// shows the session from its recorded history.
	repos := filepath.Dir(p.repo)
	noVMs, err := gitengine.New(gitengine.Config{ReposDir: repos, CheckoutsDir: filepath.Join(filepath.Dir(repos), "checkouts")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = noVMs.Close() })
	if patch, err = noVMs.RunPatch(ctx, testRun, gitengine.PatchRequest{}); err != nil || !patch.Recorded || !strings.Contains(patch.Text, "last.txt") {
		t.Fatalf("patch on a server without VMs = %+v, %v", patch, err)
	}
	if _, err = noVMs.PublishRunBranch(ctx, testRun); !errors.Is(err, guest.ErrVMNotRunning) {
		t.Fatalf("publish on a server without VMs = %v", err)
	}
}

func runGitQuiet(dir string, args ...string) string {
	out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out))
}

// lyingGit is a git service whose snapshots and objects a test replaces.
type lyingGit struct {
	*gitService
	tree    func(real string) string
	objects func(stream grpc.ServerStreamingServer[guestpb.Chunk]) error
	refused chan string
}

func (l *lyingGit) Watch(stream grpc.BidiStreamingServer[guestpb.WatchAck, guestpb.WatchEvent]) error {
	return l.gitService.Watch(lyingWatch{stream, l})
}

type lyingWatch struct {
	grpc.BidiStreamingServer[guestpb.WatchAck, guestpb.WatchEvent]
	l *lyingGit
}

func (w lyingWatch) Recv() (*guestpb.WatchAck, error) {
	ack, err := w.BidiStreamingServer.Recv()
	if err == nil && ack.GetError() != "" {
		select {
		case w.l.refused <- ack.GetError():
		default:
		}
	}
	return ack, err
}

func (w lyingWatch) Send(ev *guestpb.WatchEvent) error {
	if diff, ok := ev.GetKind().(*guestpb.WatchEvent_Diff); ok && w.l.tree != nil {
		var p events.RunDiffPayload
		if err := json.Unmarshal(diff.Diff, &p); err != nil {
			return err
		}
		p.Tree = w.l.tree(p.Tree)
		data, err := json.Marshal(p)
		if err != nil {
			return err
		}
		ev = &guestpb.WatchEvent{Seq: ev.Seq, Kind: &guestpb.WatchEvent_Diff{Diff: data}}
	}
	return w.BidiStreamingServer.Send(ev)
}

func (l *lyingGit) Objects(req *guestpb.ObjectsRequest, stream grpc.ServerStreamingServer[guestpb.Chunk]) error {
	if l.objects != nil {
		return l.objects(stream)
	}
	return l.gitService.Objects(req, stream)
}

func TestServerRefusesWhatAHostileVMSends(t *testing.T) {
	cases := map[string]*lyingGit{
		"a blob as the snapshot tree": {tree: func(string) string { return "" }},
		"a pack past the size limit": {objects: func(stream grpc.ServerStreamingServer[guestpb.Chunk]) error {
			chunk := make([]byte, guest.ObjectsChunk)
			for range gitengine.MaxSnapshotInputBytes/guest.ObjectsChunk + 2 {
				if err := stream.Send(&guestpb.Chunk{Data: chunk}); err != nil {
					return err
				}
			}
			return nil
		}},
		"a pack that is not one": {objects: func(stream grpc.ServerStreamingServer[guestpb.Chunk]) error {
			return stream.Send(&guestpb.Chunk{Data: []byte("PACK not really")})
		}},
	}
	for name, liar := range cases {
		t.Run(name, func(t *testing.T) {
			var blob string
			liar.refused = make(chan string, 1)
			p := newGitPair(t, func(real *gitService) guestpb.GitServer { liar.gitService = real; return liar })
			if liar.tree != nil {
				blob = runGit(t, p.repo, "rev-parse", p.base+":README")
				liar.tree = func(string) string { return blob }
			}
			ctx := testContext(t)
			if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p.checkout, "new.txt"), []byte("one\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			select {
			case reason := <-liar.refused:
				t.Logf("refused: %s", reason)
			case <-time.After(15 * time.Second):
				t.Fatal("the server never refused the hostile snapshot")
			}
			select {
			case ev := <-p.diffs:
				t.Fatalf("server published %+v from a hostile VM", ev.Payload)
			default:
			}
			store := filepath.Join(filepath.Dir(p.repo), "..", "checkouts", string(testRun)+".diffsnap")
			if refs := runGitQuiet(store, "for-each-ref", "refs/aether/history/"); refs != "" {
				t.Fatalf("server store recorded history from a hostile VM: %s", refs)
			}
			if _, err := p.host.RunPatch(ctx, testRun, gitengine.PatchRequest{From: blob, To: blob}); blob != "" && !errors.Is(err, gitengine.ErrInvalidObjectID) && !errors.Is(err, gitengine.ErrSnapshotTreeMissing) {
				t.Fatalf("rendering the hostile tree = %v", err)
			}
		})
	}
}
