package helper

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	livesync "github.com/3xDevOps/Aether/internal/overlay"
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
	guestDir string
	socket   string
	diffs    <-chan events.Event
	stop     func()
	// down makes the VM read as not running.
	down atomic.Bool
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

	p.guestDir = filepath.Join(root, "guest")
	p.socket = filepath.Join(t.TempDir(), "vsock.sock")
	p.serve(t, server, nil)
	git := p.git
	client, err := guest.DialGit(p.socket)
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
		VM: &gitengine.VM{Guest: func(context.Context, domain.RunID) (*guest.Git, error) {
			if p.down.Load() {
				return nil, guest.ErrVMNotRunning
			}
			return client, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.host.Close() })
	if _, p.branch, err = p.host.CreateRunCheckoutAt(ctx, testWorkspace, testRun, p.base, "main", "a task", ""); err != nil {
		t.Fatal(err)
	}
	// What the checkout's prepared disk holds.
	p.checkout = filepath.Join(p.guestDir, string(testRun))
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

// serve runs a git service over the guest directory on the pair's socket;
// server, when set, wraps it.
func (p *gitPair) serve(t *testing.T, server func(*gitService) guestpb.GitServer, mounts []guest.CheckoutMount) {
	t.Helper()
	git, err := newGitService(gitengine.Config{
		ReposDir: filepath.Join(p.guestDir, "repos"), CheckoutsDir: p.guestDir,
		QuietPeriod: 50 * time.Millisecond, MinInterval: 100 * time.Millisecond, MaxInterval: time.Second,
	}, mounts)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", p.socket)
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
	p.git, p.stop = git, func() {
		srv.Stop()
		_ = git.engine.Close()
	}
	t.Cleanup(p.stop)
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
	files   []events.FileDiffStat
	gap     atomic.Bool
	objects func(stream grpc.ServerStreamingServer[guestpb.Chunk]) error
	stage   func()
	write   func()
	refused chan string
}

func (l *lyingGit) Stage(ctx context.Context, req *guestpb.StageRequest) (*guestpb.StageReply, error) {
	if l.stage != nil {
		l.stage()
	}
	return l.gitService.Stage(ctx, req)
}

func (l *lyingGit) WriteFile(stream grpc.ClientStreamingServer[guestpb.WriteFileChunk, guestpb.WriteFileReply]) error {
	if l.write != nil {
		l.write()
	}
	return l.gitService.WriteFile(stream)
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
	diff, ok := ev.GetKind().(*guestpb.WatchEvent_Diff)
	if ok && (w.l.tree != nil || w.l.files != nil || w.l.gap.Load()) {
		var p events.RunDiffPayload
		if err := json.Unmarshal(diff.Diff, &p); err != nil {
			return err
		}
		if w.l.tree != nil {
			p.Tree = w.l.tree(p.Tree)
		}
		if w.l.gap.Load() {
			p.Tree, p.SnapshotError = "", "Snapshot history unavailable; current change statistics remain live."
		}
		if w.l.files != nil {
			p.Files = w.l.files
		}
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

func TestARestartedGitServicePicksTheRunBackUp(t *testing.T) {
	p := newGitPair(t, nil)
	ctx := testContext(t)
	if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.checkout, "before.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.nextDiff(t, hasFile("before.txt"))
	// As PID 1 starts the service again after it ended, with the checkouts
	// the kernel command line names. The stop records a history gap, which
	// the next service retries; TestKVMGitServiceBesideAFullSession kills a
	// real service, which leaves none.
	p.stop()
	if err := os.WriteFile(filepath.Join(p.checkout, "during.txt"), []byte("during\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.serve(t, nil, []guest.CheckoutMount{{Key: string(testRun), Target: p.checkout}})
	p.nextDiff(t, hasFile("during.txt"))
	if err := os.WriteFile(filepath.Join(p.checkout, "after.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.nextDiff(t, hasFile("after.txt"))
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

// TestServerPublishesABackwardReset proves publishVMHead follows an
// intentional backward move (a `git reset --hard` to an older commit) rather
// than mistaking it for a slower publish that a newer one overtook.
func TestServerPublishesABackwardReset(t *testing.T) {
	p := newGitPair(t, nil)
	ctx := testContext(t)
	if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
		t.Fatal(err)
	}
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(p.checkout, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, p.checkout, "add", name)
		runGit(t, p.checkout, "commit", "--quiet", "-m", name)
		return runGit(t, p.checkout, "rev-parse", "HEAD")
	}
	waitBranch := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if runGitQuiet(p.repo, "rev-parse", "refs/heads/"+p.branch) == want {
				return
			}
		}
		t.Fatalf("run branch never reached %s", want)
	}

	commit("first.txt")
	second := commit("second.txt")
	waitBranch(second)

	runGit(t, p.checkout, "reset", "--hard", "HEAD~1")
	first := runGit(t, p.checkout, "rev-parse", "HEAD")
	waitBranch(first)
}

// TestServerComputesDiffFilesFromTheTree proves the server derives a
// snapshot's changed-file list from the tree it verified, not from the list
// the guest claimed: a session cannot fake a file path to manufacture an
// overlap and waive a peer's messaging protection.
func TestServerComputesDiffFilesFromTheTree(t *testing.T) {
	liar := &lyingGit{files: []events.FileDiffStat{{Path: "fabricated.txt", Status: "modified", Additions: 99}}}
	p := newGitPair(t, func(real *gitService) guestpb.GitServer { liar.gitService = real; return liar })
	ctx := testContext(t)
	if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.checkout, "real.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := p.nextDiff(t, hasFile("real.txt"))
	for _, f := range diff.Files {
		if f.Path == "fabricated.txt" {
			t.Fatalf("server published the guest's fabricated file path: %+v", diff.Files)
		}
	}
}

// TestServerKeepsVerifiedFilesAcrossAGap proves that when a VM reports a
// history gap (no snapshot tree), the server republishes the last recorded
// interval's verified files and ignores the list the guest claims in the gap
// payload, so an honest session keeps its overlap and a hostile one cannot
// fake a path where it has no tree to verify.
func TestServerKeepsVerifiedFilesAcrossAGap(t *testing.T) {
	liar := &lyingGit{files: []events.FileDiffStat{{Path: "fabricated.txt", Status: "modified", Additions: 50}}}
	p := newGitPair(t, func(real *gitService) guestpb.GitServer { liar.gitService = real; return liar })
	ctx := testContext(t)
	if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.checkout, "real.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.nextDiff(t, hasFile("real.txt")) // a verified snapshot is recorded first

	liar.gap.Store(true)
	if err := os.WriteFile(filepath.Join(p.checkout, "later.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gap := p.nextDiff(t, func(d events.RunDiffPayload) bool { return d.HistoryGap })
	real, fabricated := false, false
	for _, f := range gap.Files {
		switch f.Path {
		case "real.txt":
			real = true
		case "fabricated.txt":
			fabricated = true
		}
	}
	if !real || fabricated {
		t.Fatalf("history-gap diff = %+v, want the last verified files and no fabricated path", gap.Files)
	}
}

// oversizePack builds a tiny pack holding a base blob and one REF_DELTA whose
// header claims a target of targetSize bytes. index-pack must allocate that
// whole buffer to reconstruct the result, so a few bytes drive a huge host
// allocation unless the receive is bounded.
func oversizePack(targetSize uint64) []byte {
	base := []byte("hello")
	var buf bytes.Buffer
	buf.WriteString("PACK")
	_ = binary.Write(&buf, binary.BigEndian, uint32(2))
	_ = binary.Write(&buf, binary.BigEndian, uint32(2))

	writePackObjHeader(&buf, 3, uint64(len(base)))
	writePackZlib(&buf, base)

	h := sha1.New()
	_, _ = io.WriteString(h, "blob 5\x00")
	h.Write(base)
	baseSha := h.Sum(nil)

	delta := appendPackVarint(nil, uint64(len(base)))
	delta = appendPackVarint(delta, targetSize)
	delta = append(delta, 0x01, 'x') // insert one literal byte
	writePackObjHeader(&buf, 7, uint64(len(delta)))
	buf.Write(baseSha)
	writePackZlib(&buf, delta)

	sum := sha1.Sum(buf.Bytes())
	buf.Write(sum[:])
	return buf.Bytes()
}

func writePackObjHeader(buf *bytes.Buffer, typ byte, size uint64) {
	b := typ<<4 | byte(size&0x0f)
	for size >>= 4; size > 0; size >>= 7 {
		buf.WriteByte(b | 0x80)
		b = byte(size & 0x7f)
	}
	buf.WriteByte(b)
}

func appendPackVarint(out []byte, v uint64) []byte {
	for {
		b := byte(v & 0x7f)
		if v >>= 7; v > 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}

func writePackZlib(buf *bytes.Buffer, data []byte) {
	w := zlib.NewWriter(buf)
	_, _ = w.Write(data)
	_ = w.Close()
}

// TestServerBoundsAHostilePackReceive drives both receive paths - a diff
// snapshot and a moved HEAD - with an Objects reply that is a tiny pack
// claiming an object past the fixed allocation cap. The server must refuse it
// at its git allocation limit on either path rather than letting index-pack
// allocate the whole result; the cap does not loosen for the 4 GiB history
// budget the HEAD path passes.
func TestServerBoundsAHostilePackReceive(t *testing.T) {
	const over = uint64(600 << 20) // past the 512 MiB guest-receive cap
	newLiar := func() *lyingGit {
		return &lyingGit{
			refused: make(chan string, 1),
			objects: func(stream grpc.ServerStreamingServer[guestpb.Chunk]) error {
				return stream.Send(&guestpb.Chunk{Data: oversizePack(over)})
			},
		}
	}
	expectRefused := func(t *testing.T, liar *lyingGit) {
		t.Helper()
		select {
		case reason := <-liar.refused:
			if !strings.Contains(reason, "over limit") {
				t.Fatalf("refused for %q, want a git allocation-limit failure", reason)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the server never refused the oversized pack")
		}
	}

	t.Run("snapshot", func(t *testing.T) {
		liar := newLiar()
		p := newGitPair(t, func(real *gitService) guestpb.GitServer { liar.gitService = real; return liar })
		ctx := testContext(t)
		if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p.checkout, "new.txt"), []byte("one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		expectRefused(t, liar)
		select {
		case ev := <-p.diffs:
			t.Fatalf("server published %+v from an oversized pack", ev.Payload)
		default:
		}
	})

	t.Run("published head", func(t *testing.T) {
		liar := newLiar()
		p := newGitPair(t, func(real *gitService) guestpb.GitServer { liar.gitService = real; return liar })
		ctx := testContext(t)
		if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p.checkout, "new.txt"), []byte("one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, p.checkout, "add", "new.txt")
		runGit(t, p.checkout, "commit", "--quiet", "-m", "move head")
		head := runGit(t, p.checkout, "rev-parse", "HEAD")
		expectRefused(t, liar)
		if tip := runGitQuiet(p.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.branch); tip == head {
			t.Fatal("server published a HEAD whose objects it could not safely receive")
		}
	})
}

// TestEvidenceStallDoesNotBlockTheEngine proves a session VM that stalls a
// git-service reply during evidence capture no longer freezes the engine's
// workspace-wide maintenance: another operation that needs the same
// engine-wide mutexes completes while the capture is blocked in the guest.
func TestEvidenceStallDoesNotBlockTheEngine(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	liar := &lyingGit{stage: func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}}
	p := newGitPair(t, func(real *gitService) guestpb.GitServer { liar.gitService = real; return liar })
	ctx := testContext(t)
	captured := make(chan error, 1)
	go func() {
		_, err := p.host.CaptureEvidence(ctx, testRun, "packet-1")
		captured <- err
	}()
	select {
	case <-entered:
	case err := <-captured:
		t.Fatalf("evidence capture returned before the guest object fetch: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("evidence capture never reached the guest object fetch")
	}

	// With the capture blocked in the guest, an operation that takes the
	// engine-wide mutexes must still complete promptly.
	done := make(chan error, 1)
	go func() { done <- p.host.RemoveEvidence(ctx, testWorkspace, "packet-2") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RemoveEvidence = %v", err)
		}
	case <-time.After(10 * time.Second):
		close(release)
		<-captured
		t.Fatal("RemoveEvidence blocked behind a VM stalled in evidence capture")
	}
	close(release)
	<-captured
}

// TestFilesWriteStallDoesNotBlockTheEngine proves a Files write to a VM that
// stalls in the guest no longer holds fileWriteMu, which every other write
// shares: a workspace-wide operation completes while the write is blocked.
func TestFilesWriteStallDoesNotBlockTheEngine(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	liar := &lyingGit{write: func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}}
	p := newGitPair(t, func(real *gitService) guestpb.GitServer { liar.gitService = real; return liar })
	ctx := testContext(t)
	written := make(chan error, 1)
	go func() {
		_, err := p.host.FilesWrite(ctx, testWorkspace, testRun, "", "README", []byte("edited\n"), "", domain.GitIdentity{}, nil)
		written <- err
	}()
	select {
	case <-entered:
	case err := <-written:
		t.Fatalf("Files write returned before the guest call: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("Files write never reached the guest")
	}
	done := make(chan error, 1)
	go func() { done <- p.host.RemoveEvidence(ctx, testWorkspace, "packet") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RemoveEvidence = %v", err)
		}
	case <-time.After(10 * time.Second):
		close(release)
		<-written
		t.Fatal("RemoveEvidence blocked behind a VM stalled in a Files write")
	}
	close(release)
	<-written
}

func TestGitServiceAnswersTheServersCheckoutCalls(t *testing.T) {
	p := newGitPair(t, nil)
	ctx := testContext(t)
	if err := os.WriteFile(filepath.Join(p.checkout, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("files", func(t *testing.T) {
		entries, err := p.host.FilesTree(ctx, testWorkspace, testRun, "", "")
		if err != nil || len(entries) != 2 || entries[0].Name != "README" || entries[1].Name != "work.txt" {
			t.Fatalf("tree = %+v, %v", entries, err)
		}
		read, err := p.host.FilesRead(ctx, testWorkspace, testRun, "", "README", 0)
		if err != nil || string(read.Content) != "hello\n" || read.Revision == "" {
			t.Fatalf("read = %+v, %v", read, err)
		}
		if _, err = p.host.FilesWrite(ctx, testWorkspace, testRun, "", "README", []byte("edited\n"), read.Revision, domain.GitIdentity{}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err = p.host.FilesWrite(ctx, testWorkspace, testRun, "", "README", []byte("lost\n"), read.Revision, domain.GitIdentity{}, nil); !errors.Is(err, gitengine.ErrRevisionConflict) {
			t.Fatalf("write over a changed file = %v, want a revision conflict", err)
		}
		if _, err = p.host.FilesRead(ctx, testWorkspace, testRun, "", "../etc/passwd", 0); !errors.Is(err, gitengine.ErrInvalidPath) {
			t.Fatalf("read outside the checkout = %v", err)
		}
		diff, err := p.host.FileDiff(ctx, testRun, "README")
		if err != nil || !strings.Contains(diff.Text, "+edited") {
			t.Fatalf("file diff = %+v, %v", diff, err)
		}
	})

	t.Run("evidence", func(t *testing.T) {
		revision, err := p.host.CaptureEvidence(ctx, testRun, "packet1")
		if err != nil {
			t.Fatal(err)
		}
		if got := runGit(t, p.repo, "show", revision.Commit+":work.txt"); got != "work" {
			t.Fatalf("evidence holds work.txt as %q", got)
		}
	})

	t.Run("commit", func(t *testing.T) {
		if _, err := exec.LookPath("ssh-keygen"); err != nil {
			t.Skip("ssh-keygen is not on PATH")
		}
		keyPath := filepath.Join(t.TempDir(), "key")
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v: %s", err, out)
		}
		key, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		head := runGit(t, p.checkout, "rev-parse", "HEAD")
		member := domain.GitIdentity{Name: "Member", Email: "member@localhost"}
		commit, err := p.host.CommitAll(ctx, testRun, "aether: a task  \n\n", member, key)
		if err != nil {
			t.Fatal(err)
		}
		if got := runGit(t, p.checkout, "rev-parse", "HEAD"); got != commit {
			t.Fatalf("checkout HEAD is %s, want the server's commit %s", got, commit)
		}
		if parent := runGit(t, p.checkout, "rev-parse", "HEAD^"); parent != head {
			t.Fatalf("commit's parent is %s, want %s", parent, head)
		}
		object := runGit(t, p.checkout, "cat-file", "commit", commit)
		for _, want := range []string{"author Member <member@localhost>", "committer Aether <aether@localhost>", "gpgsig -----BEGIN SSH SIGNATURE", "\n\naether: a task\n"} {
			if !strings.Contains(object+"\n", want) {
				t.Errorf("commit lacks %q:\n%s", want, object)
			}
		}
		if status := runGit(t, p.checkout, "status", "--porcelain"); status != "" {
			t.Errorf("checkout is not clean after the commit: %q", status)
		}
		if again, err := p.host.CommitAll(ctx, testRun, "aether: again", member, nil); err != nil || again != "" {
			t.Errorf("committing a clean checkout = %q, %v", again, err)
		}
		if published, err := p.host.PublishRunBranch(ctx, testRun); err != nil || published != commit {
			t.Errorf("publish = %s, %v", published, err)
		}
	})

	t.Run("commit past the snapshot bound", func(t *testing.T) {
		// commitAll has no size bound, so neither has a VM's.
		big, err := os.Create(filepath.Join(p.checkout, "big.bin"))
		if err == nil {
			err = errors.Join(big.Truncate(gitengine.MaxSnapshotInputBytes+(2<<20)), big.Close())
		}
		if err != nil {
			t.Fatal(err)
		}
		commit, err := p.host.CommitAll(ctx, testRun, "wip: big", domain.GitIdentity{}, nil)
		if err != nil || commit == "" {
			t.Fatalf("commit of a checkout over %d bytes = %q, %v", gitengine.MaxSnapshotInputBytes, commit, err)
		}
		if got := runGit(t, p.checkout, "ls-tree", "--name-only", "HEAD", "big.bin"); got != "big.bin" {
			t.Fatalf("the commit lacks big.bin: %q", got)
		}
	})

	t.Run("subsession fetch", func(t *testing.T) {
		child := filepath.Join(t.TempDir(), "child")
		runGit(t, filepath.Dir(child), "clone", "--quiet", p.repo, child)
		if err := os.WriteFile(filepath.Join(child, "child.txt"), []byte("child\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, child, "add", "child.txt")
		runGit(t, child, "commit", "--quiet", "-m", "child work")
		runGit(t, child, "push", "--quiet", "origin", "HEAD:refs/heads/aether/run-child")
		want := runGit(t, child, "rev-parse", "HEAD")
		got, err := p.host.FetchRunBranch(ctx, testWorkspace, "aether/run-child", testRun, "refs/remotes/children/c1")
		if err != nil || got != want {
			t.Fatalf("fetch = %s, %v; want %s", got, err, want)
		}
		if ref := runGit(t, p.checkout, "rev-parse", "refs/remotes/children/c1"); ref != want {
			t.Fatalf("checkout's children ref is %s, want %s", ref, want)
		}
	})

	t.Run("live sync", func(t *testing.T) {
		local := t.TempDir()
		if err := os.WriteFile(filepath.Join(local, "local.txt"), []byte("from local"), 0o644); err != nil {
			t.Fatal(err)
		}
		client, err := guest.DialGit(p.socket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		sess, err := livesync.NewSession(livesync.Options{LocalDir: local, DataDir: t.TempDir(), Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
			return client.Sync(ctx, string(testRun))
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sess.Close)
		runCtx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := sess.Start(runCtx, string(testRun)); err != nil {
			t.Fatal(err)
		}
		go func() { _ = sess.Run(runCtx) }()
		deadline := time.Now().Add(20 * time.Second)
		for {
			if data, err := os.ReadFile(filepath.Join(p.checkout, "local.txt")); err == nil && string(data) == "from local" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the overlay's file never reached the checkout")
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	t.Run("a sync client that goes away", func(t *testing.T) {
		server, client := net.Pipe()
		served := make(chan error, 1)
		go func() { served <- p.host.ServeVMSync(ctx, testRun, server) }()
		_ = client.Close()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Fatal("the relay kept the VM's sync endpoint after its client left")
		}
	})
}

func TestStoppedVMSessionIsShownFromItsLastSnapshot(t *testing.T) {
	p := newGitPair(t, nil)
	ctx := testContext(t)
	if err := p.host.StartDiffWatch(ctx, testWorkspace, testRun); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.checkout, "README"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.checkout, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README", filepath.Join(p.checkout, "link")); err != nil {
		t.Fatal(err)
	}
	p.host.StopDiffWatch(testRun)
	p.down.Store(true)

	entries, err := p.host.FilesTree(ctx, testWorkspace, testRun, "", "")
	if err != nil || len(entries) != 2 || entries[1].Name != "new.txt" {
		t.Fatalf("tree of a stopped VM = %+v, %v", entries, err)
	}
	read, err := p.host.FilesRead(ctx, testWorkspace, testRun, "", "README", 0)
	if err != nil || string(read.Content) != "changed\n" {
		t.Fatalf("read of a stopped VM = %+v, %v", read, err)
	}
	diff, err := p.host.FileDiff(ctx, testRun, "README")
	if err != nil || !strings.Contains(diff.Text, "+changed") || !diff.Recorded {
		t.Fatalf("file diff of a stopped VM = %+v, %v", diff, err)
	}
	if _, err = p.host.FilesWrite(ctx, testWorkspace, testRun, "", "README", []byte("x\n"), "", domain.GitIdentity{}, nil); !errors.Is(err, guest.ErrVMNotRunning) {
		t.Fatalf("write to a stopped VM = %v", err)
	}
	// Only the VM can read its uncommitted work, which cleanup must keep.
	if _, err = p.host.CaptureEvidence(ctx, testRun, "uncommitted"); err == nil || errors.Is(err, gitengine.ErrCheckoutNotFound) {
		t.Fatalf("evidence of a stopped VM's uncommitted checkout = %v, want an error that keeps the checkout", err)
	}
	p.down.Store(false)
	commit, err := p.host.CommitAll(ctx, testRun, "wip: a task", domain.GitIdentity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.down.Store(true)
	revision, err := p.host.CaptureEvidence(ctx, testRun, "committed")
	if err != nil || revision.Tree != runGit(t, p.checkout, "rev-parse", commit+"^{tree}") {
		t.Fatalf("evidence of a stopped VM after its commit = %+v, %v", revision, err)
	}

	if err = p.host.RemoveRunCheckout(ctx, testRun); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(filepath.Dir(p.repo), "..", "checkouts", string(testRun)+".diffsnap")
	if _, err = os.Stat(filepath.Join(store, "objects", "info", "alternates")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the removed checkout's history still borrows the workspace's objects: %v", err)
	}
	if err = os.RemoveAll(p.repo); err != nil {
		t.Fatal(err)
	}
	patch, err := p.host.RunPatch(ctx, testRun, gitengine.PatchRequest{})
	if err != nil || !patch.Recorded || !strings.Contains(patch.Text, "-hello") || !strings.Contains(patch.Text, "+new") {
		t.Fatalf("patch of a removed checkout without its workspace = %+v, %v", patch, err)
	}
}
