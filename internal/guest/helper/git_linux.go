package helper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"github.com/mdlayher/vsock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// gitDir holds the checkouts the git service works on, the identity
	// of each and its snapshot store, on the data disk and out of the
	// session's reach.
	gitDir = dataRoot + "/git"
	// disksDir is where each checkout's disk is mounted.
	disksDir = privateDir + "/checkouts"
	// changedInterval spaces the file change reports of one watch.
	changedInterval = time.Second
	preparedHead    = "ref: refs/heads/" + gitengine.PreparedBranch + "\n"
	// gitServiceArg starts the git service, which runs as a process of its
	// own: PID 1 reaps every child it has, which would take the exit of a
	// git the engine started from under the engine's own wait.
	gitServiceArg = "git-service"
)

// gitLimits bound the memory of every git the service runs: a file over
// 16 MiB is streamed rather than read whole, and pack-objects keeps one
// thread and 64 MiB of delta candidates.
var gitLimits = []string{"core.bigFileThreshold=16m", "pack.windowMemory=64m", "pack.threads=1"}

// superviseGit runs the git service in cgroup and starts it again whenever
// it ends, waiting longer after each start that did not last.
func superviseGit(procs *processes, cgroup *os.File) {
	delay := time.Second
	for n := 0; ; n++ {
		started := time.Now()
		p, err := procs.spawn(spawnSpec{
			ProcessSpec: guest.ProcessSpec{
				ID:   "git-" + strconv.Itoa(n),
				Argv: []string{"/proc/self/exe", "guest", gitServiceArg},
				// The heap goal keeps garbage from crowding the git
				// service's cgroup.
				Env: []string{"PATH=" + defaultPath, "GOMEMLIMIT=64MiB"},
			},
			log: os.Stderr, cgroup: cgroup,
		})
		if err != nil {
			slog.Error("guest: start the git service", "error", err)
		} else {
			<-p.done
			// A git the dead service started would race the next service
			// over the same files.
			if killErr := os.WriteFile(filepath.Join(cgroup.Name(), "cgroup.kill"), []byte("1"), 0); killErr != nil {
				slog.Error("guest: end what the git service started", "error", killErr)
			}
			slog.Warn("guest: the git service exited; starting it again", "code", p.code, "in", delay)
		}
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		time.Sleep(delay)
		delay = min(2*delay, 30*time.Second)
	}
}

// serveGit is the git service's process: it serves the checkouts the kernel
// command line mounted.
func serveGit() error {
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return err
	}
	cfg, err := guest.ParseCmdline(string(cmdline))
	if err != nil {
		return err
	}
	// A live overlay's endpoint keeps its staging there.
	if err = os.Setenv("MUTAGEN_DATA_DIRECTORY", filepath.Join(gitDir, "mutagen")); err != nil {
		return err
	}
	svc, err := newGitService(gitengine.Config{ReposDir: filepath.Join(gitDir, "repos"), CheckoutsDir: gitDir, GitConfig: gitLimits}, cfg.Checkouts)
	if err != nil {
		return err
	}
	listener, err := vsock.Listen(guest.GitPort, nil)
	if err != nil {
		return fmt.Errorf("listen on vsock port %d: %w", guest.GitPort, err)
	}
	// A guest short of memory can be slow to read the host's ping reply,
	// and a connection the ping drops fails the long git work it carries.
	server := newGRPCServer(8<<20, time.Minute)
	guestpb.RegisterGitServer(server, svc)
	return server.Serve(hostOnly{listener})
}

// endSession kills every process left in the session cgroup once the main
// process exited, as a container's end kills its processes: the VM may stay
// up a while for the server's last git work, which nothing of the session
// may change meanwhile.
func endSession(cgroup *sessionCgroup) {
	if cgroup == nil {
		return
	}
	if err := os.WriteFile(filepath.Join(cgroup.dir, "cgroup.kill"), []byte("1"), 0); err != nil {
		slog.Warn("guest: end the session's processes after the main process exited", "error", err)
	}
}

// dropCaches hands the guest's clean page cache back to the host. Writing
// the root cgroup's memory.reclaim from the helper, which serves the home's
// FUSE mount, instead stalls on the home's own pages.
func dropCaches() {
	if err := os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0); err != nil {
		slog.Warn("guest: drop the page cache", "error", err)
	}
}

// mountCheckouts mounts each checkout's disk and binds its checkout under
// gitDir, for the git service, and at its target, for the session.
func mountCheckouts(mounts []guest.CheckoutMount) error {
	if len(mounts) == 0 {
		return nil
	}
	if err := mountVolume(guest.VolumeData, dataRoot); err != nil {
		return fmt.Errorf("mount the data volume: %w", err)
	}
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		return err
	}
	var errs []error
	for _, m := range mounts {
		if err := mountCheckout(m); err != nil {
			errs = append(errs, fmt.Errorf("mount checkout %s at %s: %w", m.Key, m.Target, err))
		}
	}
	return errors.Join(errs...)
}

func mountCheckout(m guest.CheckoutMount) error {
	device, _, err := diskBySerial(m.Serial)
	if err != nil {
		return err
	}
	disk := filepath.Join(disksDir, m.Key)
	if err := mountOnce(mountSpec{device, disk, "ext4", 0, guestDiskOptions}); err != nil {
		return err
	}
	for _, target := range []string{filepath.Join(gitDir, m.Key), m.Target} {
		if err := bind(disk, guest.Mount{Volume: guest.VolumeData, Source: gitengine.PreparedDir, Target: target}); err != nil {
			return err
		}
	}
	return nil
}

// gitService runs the server's git engine over the VM's checkouts. Its
// engine relays each snapshot and moved HEAD to the server through the
// checkout's Watch, and counts it as delivered once the server says so.
type gitService struct {
	guestpb.UnimplementedGitServer
	engine *gitengine.Engine
	dir    string
	// targets are where the session sees each checkout; the checkout's
	// owner cannot reach dir.
	targets map[string]string

	mu         sync.Mutex
	watches    map[string]*gitWatch
	workspaces map[string]domain.WorkspaceID
	seq        atomic.Uint64
}

type gitWatch struct {
	stream grpc.BidiStreamingServer[guestpb.WatchAck, guestpb.WatchEvent]
	sendMu sync.Mutex
	mu     sync.Mutex
	acks   map[uint64]chan string
	done   chan struct{}
}

// newGitService runs an engine over the checkouts in cfg.CheckoutsDir,
// which mounts placed there.
func newGitService(cfg gitengine.Config, mounts []guest.CheckoutMount) (*gitService, error) {
	s := &gitService{
		dir: cfg.CheckoutsDir, targets: map[string]string{},
		watches: map[string]*gitWatch{}, workspaces: map[string]domain.WorkspaceID{},
	}
	for _, m := range mounts {
		s.targets[m.Key] = m.Target
	}
	cfg.Relay = s
	engine, err := gitengine.New(cfg)
	if err != nil {
		return nil, err
	}
	s.engine = engine
	// A service that started again after a crash knows the checkouts Setup
	// adopted from their identities on the data disk.
	for _, m := range mounts {
		if ws, wsErr := engine.CheckoutWorkspace(domain.RunID(m.Key)); wsErr == nil {
			s.workspaces[m.Key] = ws
		}
		if err = engine.ClearStaleLocks(domain.RunID(m.Key)); err != nil {
			return nil, fmt.Errorf("checkout %s: %w", m.Key, err)
		}
	}
	return s, nil
}

// Setup gives each checkout on its first boot the run branch, records its
// identity and starts its watch, all before the session's first process.
func (s *gitService) Setup(ctx context.Context, req *guestpb.SetupRequest) (*guestpb.SetupReply, error) {
	if err := s.setup(ctx, req.GetCheckouts()); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "set up checkouts: %v", err)
	}
	return &guestpb.SetupReply{}, nil
}

func (s *gitService) setup(ctx context.Context, checkouts []*guestpb.Checkout) error {
	for _, c := range checkouts {
		id := guest.Checkout{
			Key: c.GetKey(), Workspace: c.GetWorkspace(), Branch: c.GetBranch(), Base: c.GetBase(),
			BaseBranch: c.GetBaseBranch(), Origin: c.GetOrigin(), UID: int(c.GetUid()), GID: int(c.GetGid()),
		}
		if !guest.ValidCheckoutKey(id.Key) {
			return fmt.Errorf("checkout key %q is invalid", id.Key)
		}
		if err := ownCheckout(filepath.Join(s.dir, id.Key), id.UID, id.GID); err != nil {
			return fmt.Errorf("checkout %s: %w", id.Key, err)
		}
		dir, ok := s.targets[id.Key]
		if !ok {
			dir = filepath.Join(s.dir, id.Key)
		}
		if err := prepareBranch(dir, id); err != nil {
			return fmt.Errorf("checkout %s: %w", id.Key, err)
		}
		if err := s.engine.AdoptCheckout(id); err != nil {
			return err
		}
		if err := s.engine.WatchCheckout(ctx, domain.WorkspaceID(id.Workspace), domain.RunID(id.Key)); err != nil {
			return fmt.Errorf("checkout %s: %w", id.Key, err)
		}
		s.mu.Lock()
		s.workspaces[id.Key] = domain.WorkspaceID(id.Workspace)
		s.mu.Unlock()
	}
	return nil
}

// ownCheckout gives the checkout at dir to the session user when its top
// directory belongs to someone else, as after the session started again
// from an image with another user.
func ownCheckout(dir string, uid, gid int) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if st := info.Sys().(*syscall.Stat_t); int(st.Uid) == uid && int(st.Gid) == gid {
		return nil
	}
	return gitengine.ChownCheckout(dir, uid, gid)
}

// prepareBranch turns a checkout still on its prepared disk's branch into
// the run's, as the checkout's owner, so every file git writes stays the
// session's. A checkout that booted before keeps whatever the session made
// of it.
func prepareBranch(dir string, id guest.Checkout) error {
	head, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil || string(head) != preparedHead {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	git := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Env = []string{"PATH=" + defaultPath, "HOME=/", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid, NoSetGroups: true}}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %w: %s", args, err, out)
		}
		return nil
	}
	steps := [][]string{
		{"config", "aether.base", id.Base},
		{"config", "aether.branch", id.Branch},
		{"config", "aether.workspace", id.Workspace},
	}
	if id.Origin != "" {
		steps = append(steps, []string{"remote", "set-url", "origin", id.Origin})
	}
	// The rename comes last: until it, the next boot sets the checkout up
	// again.
	steps = append(steps, []string{"branch", "-m", gitengine.PreparedBranch, id.Branch})
	for _, args := range steps {
		if err := git(args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *gitService) workspace(key string) (domain.WorkspaceID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, ok := s.workspaces[key]
	if !ok {
		return "", status.Errorf(codes.NotFound, "no checkout %q", key)
	}
	return ws, nil
}

func (s *gitService) Watch(stream grpc.BidiStreamingServer[guestpb.WatchAck, guestpb.WatchEvent]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	key := first.GetKey()
	ws, err := s.workspace(key)
	if err != nil {
		return err
	}
	if err := s.engine.WatchCheckout(stream.Context(), ws, domain.RunID(key)); err != nil {
		return gitStatus(err)
	}
	w := &gitWatch{stream: stream, acks: map[uint64]chan string{}, done: make(chan struct{})}
	s.mu.Lock()
	if old := s.watches[key]; old != nil {
		old.close()
	}
	s.watches[key] = w
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.watches[key] == w {
			delete(s.watches, key)
		}
		s.mu.Unlock()
		w.close()
	}()
	go s.reportChanges(stream.Context(), key, w)
	received := make(chan error, 1)
	go func() {
		for {
			ack, err := stream.Recv()
			if err != nil {
				received <- err
				return
			}
			w.mu.Lock()
			reply := w.acks[ack.GetSeq()]
			delete(w.acks, ack.GetSeq())
			w.mu.Unlock()
			if reply != nil {
				reply <- ack.GetError()
			}
		}
	}()
	select {
	case err := <-received:
		return err
	case <-w.done:
		return status.Error(codes.Aborted, "another watch of this checkout replaced this one")
	}
}

func (w *gitWatch) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.done:
	default:
		close(w.done)
	}
}

func (w *gitWatch) send(ev *guestpb.WatchEvent) error {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	return w.stream.Send(ev)
}

// reportChanges tells the server, at most once per changedInterval, that a
// file of the checkout changed.
func (s *gitService) reportChanges(ctx context.Context, key string, w *gitWatch) {
	tick := time.NewTicker(changedInterval)
	defer tick.Stop()
	var reported time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case <-tick.C:
		}
		changed, ok := s.engine.LastFileChange(domain.RunID(key))
		if !ok || !changed.After(reported) {
			continue
		}
		if err := w.send(&guestpb.WatchEvent{Kind: &guestpb.WatchEvent_Changed{Changed: true}}); err != nil {
			return
		}
		reported = changed
	}
}

// deliver sends ev on key's watch and waits for the server's answer. It
// fails at once while no server watches: the engine holds the checkout's
// snapshot lock meanwhile, and records a gap that it retries.
func (s *gitService) deliver(ctx context.Context, key domain.RunID, ev *guestpb.WatchEvent) error {
	s.mu.Lock()
	w := s.watches[string(key)]
	s.mu.Unlock()
	if w == nil {
		return fmt.Errorf("no server watches checkout %s", key)
	}
	err := s.deliverTo(ctx, w, ev)
	if errors.Is(err, errWatchGone) {
		return fmt.Errorf("checkout %s: %w", key, err)
	}
	return err
}

var errWatchGone = errors.New("the server's watch ended")

func (s *gitService) deliverTo(ctx context.Context, w *gitWatch, ev *guestpb.WatchEvent) error {
	ev.Seq = s.seq.Add(1)
	reply := make(chan string, 1)
	w.mu.Lock()
	w.acks[ev.Seq] = reply
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.acks, ev.Seq)
		w.mu.Unlock()
	}()
	if err := w.send(ev); err != nil {
		return errWatchGone
	}
	select {
	case refused := <-reply:
		if refused != "" {
			return fmt.Errorf("the server refused the snapshot: %s", refused)
		}
		return nil
	case <-w.done:
		return errWatchGone
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *gitService) Snapshot(ctx context.Context, key domain.RunID, payload events.RunDiffPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > guest.MaxDiffEventBytes {
		return fmt.Errorf("diff snapshot of %d bytes exceeds %d", len(data), guest.MaxDiffEventBytes)
	}
	return s.deliver(ctx, key, &guestpb.WatchEvent{Kind: &guestpb.WatchEvent_Diff{Diff: data}})
}

func (s *gitService) BranchMoved(ctx context.Context, key domain.RunID, head string) error {
	return s.deliver(ctx, key, &guestpb.WatchEvent{Kind: &guestpb.WatchEvent_Head{Head: head}})
}

func (s *gitService) Flush(_ context.Context, req *guestpb.FlushRequest) (*guestpb.FlushReply, error) {
	if _, err := s.workspace(req.GetKey()); err != nil {
		return nil, err
	}
	s.engine.FinishWatch(domain.RunID(req.GetKey()))
	return &guestpb.FlushReply{}, nil
}

func (s *gitService) Objects(req *guestpb.ObjectsRequest, stream grpc.ServerStreamingServer[guestpb.Chunk]) error {
	if _, err := s.workspace(req.GetKey()); err != nil {
		return err
	}
	out := bufio.NewWriterSize(chunkWriter{stream}, guest.ObjectsChunk)
	if err := s.engine.PackObjects(stream.Context(), domain.RunID(req.GetKey()), req.GetWants(), req.GetHaves(), out); err != nil {
		return gitStatus(err)
	}
	return out.Flush()
}

type chunkWriter struct {
	stream grpc.ServerStreamingServer[guestpb.Chunk]
}

func (w chunkWriter) Write(p []byte) (int, error) {
	if err := w.stream.Send(&guestpb.Chunk{Data: p}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *gitService) Patch(ctx context.Context, req *guestpb.PatchRequest) (*guestpb.PatchReply, error) {
	if _, err := s.workspace(req.GetKey()); err != nil {
		return nil, err
	}
	p, err := s.engine.RunPatch(ctx, domain.RunID(req.GetKey()), gitengine.PatchRequest{From: req.GetFrom(), MaxBytes: int(req.GetMaxBytes())})
	if err != nil {
		return nil, gitStatus(err)
	}
	return &guestpb.PatchReply{Base: p.Base, Text: p.Text, Truncated: p.Truncated, ForkTree: p.ForkTree}, nil
}

func (s *gitService) Head(ctx context.Context, req *guestpb.HeadRequest) (*guestpb.HeadReply, error) {
	if _, err := s.workspace(req.GetKey()); err != nil {
		return nil, err
	}
	head, err := s.engine.CheckoutHead(ctx, domain.RunID(req.GetKey()))
	if err != nil {
		return nil, gitStatus(err)
	}
	return &guestpb.HeadReply{Commit: head}, nil
}

// gitStatus keeps the reason of an engine error the server has a name for.
func gitStatus(err error) error {
	if reason := gitengine.GitReason(err); reason != "" {
		return guest.GitStatus(&guest.GitError{Reason: reason, Message: err.Error()})
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return guest.GitStatus(err)
}
