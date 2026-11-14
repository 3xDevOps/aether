//go:build kvm

package vm

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// The checkout tests boot real session VMs as kvm_integration_test.go does,
// with the same AETHER_VM_TEST_* variables, from AETHER_TEST_IMAGE or else
// an image with git.

const checkoutImage = "docker.io/library/buildpack-deps:bookworm-scm"

type checkoutHost struct {
	e     *engine
	git   *gitengine.Engine
	repos string
	diffs <-chan events.Event
}

func newCheckoutHost(t *testing.T) *checkoutHost {
	t.Helper()
	dir, kernel, server := os.Getenv("AETHER_VM_TEST_DIR"), os.Getenv("AETHER_VM_TEST_KERNEL"), os.Getenv("AETHER_VM_TEST_SERVER")
	if dir == "" || kernel == "" || server == "" || os.Geteuid() != 0 {
		t.Skip("set AETHER_VM_TEST_DIR, AETHER_VM_TEST_KERNEL and AETHER_VM_TEST_SERVER and run as root to boot real VMs")
	}
	images, err := vmimage.Open(filepath.Join(dir, "vm", "images"), kernel, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &checkoutHost{repos: filepath.Join(dir, "repos")}
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunDiff}}})
	if err != nil {
		t.Fatal(err)
	}
	h.diffs = sub.Events()
	h.e = newEngine(Config{
		DataDir: dir, Images: images, Monitor: []string{server, MonitorCommand}, Helper: server,
		CheckoutDisks: func(ctx context.Context, path, target string, uid, gid int) (string, guest.Checkout, bool, error) {
			return h.git.VMCheckoutDisk(ctx, path, target, uid, gid)
		},
	}, systemdUnits{}, dialHelper)
	vms := &Runtime{vms: h.e}
	h.git, err = gitengine.New(gitengine.Config{
		ReposDir: h.repos, CheckoutsDir: filepath.Join(dir, "checkouts"), Bus: bus,
		VM: &gitengine.VM{Disks: images, Guest: vms.Git},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.git.Close() })
	return h
}

func hostGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@localhost"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// workspace makes a workspace repository of n files and returns its main
// commit.
func (h *checkoutHost) workspace(t *testing.T, ws domain.WorkspaceID, n int) string {
	t.Helper()
	repo := filepath.Join(h.repos, string(ws)+".git")
	if _, err := os.Stat(repo); err == nil {
		return hostGit(t, repo, "rev-parse", "main")
	}
	seed := t.TempDir()
	hostGit(t, seed, "init", "--quiet", "--initial-branch=main")
	for i := range n {
		sub := filepath.Join(seed, "src", strconv.Itoa(i%200))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%d.txt", i)), []byte(fmt.Sprintf("file %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hostGit(t, seed, "add", ".")
	hostGit(t, seed, "commit", "--quiet", "-m", "seed")
	hostGit(t, h.repos, "clone", "--quiet", "--bare", seed, repo)
	return hostGit(t, repo, "rev-parse", "main")
}

func (h *checkoutHost) session(t *testing.T, ws domain.WorkspaceID, key, base string, mounts ...runtime.Mount) time.Duration {
	t.Helper()
	t.Cleanup(func() { _ = h.e.destroy(context.Background(), key) })
	t.Cleanup(func() { _ = h.git.RemoveRunHistory(context.Background(), domain.RunID(key)) })
	checkout, _, err := h.git.CreateRunCheckoutAt(t.Context(), ws, domain.RunID(key), base, "main", "a task", "")
	if err != nil {
		t.Fatal(err)
	}
	created := h.boot(t, key, checkout, "1000:1000", mounts...)
	if err := h.git.StartDiffWatch(t.Context(), ws, domain.RunID(key)); err != nil {
		t.Fatal(err)
	}
	return created
}

// boot creates and starts the VM of the session key as user, and returns
// how long the create took.
func (h *checkoutHost) boot(t *testing.T, key, checkout, user string, mounts ...runtime.Mount) time.Duration {
	t.Helper()
	spec := runtime.Spec{
		Image: cmp.Or(os.Getenv("AETHER_TEST_IMAGE"), checkoutImage), CreationKey: key, TTY: true, User: user,
		Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env:     map[string]string{"AETHER_RUN_ID": key, "HOME": "/home/aether"}, CPULimit: 2, MemoryLimitBytes: 1 << 30,
		WorktreeHostPath: checkout, WorktreeMountPath: "/workspace", WorkingDir: "/workspace", Mounts: mounts,
	}
	start := time.Now()
	if _, err := h.e.create(t.Context(), key, spec, options{}); err != nil {
		t.Fatal(err)
	}
	created := time.Since(start)
	if err := h.e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	return created
}

func (h *checkoutHost) sh(t *testing.T, key, script string) string {
	t.Helper()
	code, out, errOut, err := h.e.exec(t.Context(), key, []string{"/bin/sh", "-c", script}, "/workspace")
	if err != nil || code != 0 {
		t.Fatalf("%s: %d %v: %s%s", script, code, err, out, errOut)
	}
	return strings.TrimSpace(out)
}

func TestKVMCheckoutLivesOnTheVMDisk(t *testing.T) {
	h := newCheckoutHost(t)
	const files = 40000
	ws := domain.WorkspaceID("wscheckout" + strconv.Itoa(files))
	base := h.workspace(t, ws, files)
	prefix := fmt.Sprintf("run-vmco-%x", time.Now().Unix())
	first := h.session(t, ws, prefix+"-a", base)
	second := h.session(t, ws, prefix+"-b", base)
	t.Logf("%d files: the first checkout disk took %s with its prepared disk, the second %s", files, first, second)
	if second > 2*time.Second {
		t.Errorf("a checkout over an existing prepared disk took %s", second)
	}
	// Started again as another user, as from an older image after its own
	// failed to start, a session gets its checkout handed over.
	other := prefix + "-b"
	if err := h.e.destroy(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	h.boot(t, other, filepath.Join(filepath.Dir(h.repos), "checkouts", other), "1001:1001")
	if owner := h.sh(t, other, "stat -c %u:%g /workspace /workspace/src/0/f0.txt /workspace/.git/index | sort -u"); owner != "1001:1001" {
		t.Errorf("the checkout of a session started again as 1001:1001 is owned by %q", owner)
	}

	key := prefix + "-a"
	run := domain.RunID(key)
	branch := h.sh(t, key, "git symbolic-ref --short HEAD")
	if !strings.HasPrefix(branch, "aether/run-") {
		t.Fatalf("checkout is on %q, not its run branch", branch)
	}
	if owner := h.sh(t, key, "stat -c %u:%g /workspace /workspace/src/0/f0.txt /workspace/.git/index | sort -u"); owner != "1000:1000" {
		t.Errorf("checkout files are owned by %q", owner)
	}
	h.sh(t, key, "echo one > new.txt && echo two >> src/0/f0.txt")
	waitDiff(t, h.diffs, "new.txt", time.Minute)
	patch, err := h.git.RunPatch(t.Context(), run, gitengine.PatchRequest{})
	if err != nil || !strings.Contains(patch.Text, "+++ b/new.txt") || !strings.Contains(patch.Text, "+two") {
		t.Fatalf("live patch = %+v, %v", patch, err)
	}

	h.sh(t, key, "git add -A && git -c user.name=Agent -c user.email=agent@localhost commit -qm 'agent work'")
	head := h.sh(t, key, "git rev-parse HEAD")
	repo := filepath.Join(h.repos, string(ws)+".git")
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, _ := exec.Command("git", "-C", repo, "rev-parse", "refs/heads/"+branch).Output()
		if strings.TrimSpace(string(out)) == head {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run branch %s never reached the VM's commit %s", branch, head)
		}
		time.Sleep(200 * time.Millisecond)
	}

	ctx := t.Context()
	entries, err := h.git.FilesTree(ctx, ws, run, "", "")
	if err != nil || !hasEntry(entries, "new.txt") || !hasEntry(entries, "src") {
		t.Fatalf("files tree = %+v, %v", entries, err)
	}
	read, err := h.git.FilesRead(ctx, ws, run, "", "new.txt", 0)
	if err != nil || string(read.Content) != "one\n" {
		t.Fatalf("files read = %+v, %v", read, err)
	}
	upload := []byte{0, 1, 2, 'x', '\n'}
	if _, err = h.git.FilesWrite(ctx, ws, run, "", "uploads/blob.bin", upload, "", domain.GitIdentity{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := h.sh(t, key, "od -An -tx1 uploads/blob.bin | tr -d ' \n'; stat -c ' %u' uploads/blob.bin"); got != "000102780a 1000" {
		t.Fatalf("uploaded file in the VM: %q", got)
	}
	diff, err := h.git.FileDiff(ctx, run, "src/0/f0.txt")
	if err != nil || !strings.Contains(diff.Text, "+two") {
		t.Fatalf("file diff = %+v, %v", diff, err)
	}
	child := t.TempDir()
	hostGit(t, h.repos, "clone", "--quiet", repo, child)
	hostGit(t, child, "commit", "--quiet", "--allow-empty", "-m", "subsession work")
	hostGit(t, child, "push", "--quiet", "origin", "HEAD:refs/heads/aether/run-vmco-child-"+prefix)
	childHead := hostGit(t, child, "rev-parse", "HEAD")
	fetched, err := h.git.FetchRunBranch(ctx, ws, "aether/run-vmco-child-"+prefix, run, "refs/remotes/children/c1")
	if err != nil || fetched != childHead {
		t.Fatalf("subsession fetch = %s, %v", fetched, err)
	}
	if got := h.sh(t, key, "git rev-parse refs/remotes/children/c1"); got != childHead {
		t.Fatalf("the VM's children ref is %s, want %s", got, childHead)
	}

	// Pause freezes the session, not the VM: a writer in it stops, exec is
	// refused, and git still serves the checkout, so a close can commit a
	// paused session as it commits a paused container.
	h.sh(t, key, "(while [ ! -e stop ]; do date +%s%N > tick; sleep 0.05; done) >/dev/null 2>&1 &")
	tick := func() string {
		t.Helper()
		got, readErr := h.git.FilesRead(ctx, ws, run, "", "tick", 0)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(got.Content)
	}
	if err = h.e.pause(ctx, key); err != nil {
		t.Fatal(err)
	}
	frozen := tick()
	time.Sleep(300 * time.Millisecond)
	if now := tick(); now != frozen {
		t.Fatalf("a paused session still writes: %q then %q", frozen, now)
	}
	if _, _, _, err = h.e.exec(ctx, key, []string{"true"}, "/"); err == nil {
		t.Fatal("exec into a paused session was not refused")
	}
	if patch, err = h.git.RunPatch(ctx, run, gitengine.PatchRequest{}); err != nil || patch.Recorded || !strings.Contains(patch.Text, "+++ b/tick") {
		t.Fatalf("patch of a paused session = %+v, %v", patch, err)
	}
	member := domain.GitIdentity{Name: "Member", Email: "member@localhost"}
	pausedCommit, err := h.git.CommitAll(ctx, run, "wip: a task", member, nil)
	if err != nil || pausedCommit == "" {
		t.Fatalf("commit of a paused session = %q, %v", pausedCommit, err)
	}
	store := filepath.Join(filepath.Dir(h.repos), "checkouts", key+".diffsnap")
	if got := hostGit(t, store, "show", pausedCommit+":tick"); got != strings.TrimSpace(frozen) || tick() != frozen {
		t.Fatalf("the paused session's commit holds tick %q, the checkout %q; both should be %q", got, tick(), frozen)
	}
	head = pausedCommit
	if err = h.e.resume(ctx, key); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if now := tick(); now == frozen {
		t.Fatal("a resumed session does not write")
	}
	h.sh(t, key, "touch stop && sleep 0.3 && rm -f stop tick")

	// Finalize's order: the main process exits, then the last snapshot, the
	// commit, publication and evidence, then the VM goes.
	h.sh(t, key, "echo after > after-commit.txt")
	if err = h.e.stop(ctx, key, time.Second); err != nil {
		t.Fatal(err)
	}
	h.git.StopDiffWatch(run)
	if got := hostGit(t, store, "ls-tree", "--name-only", "refs/aether/published", "after-commit.txt"); got != "after-commit.txt" {
		t.Fatalf("the last snapshot, taken after the main process exited, lacks the last file: %q", got)
	}
	final, err := h.git.CommitAll(ctx, run, "aether: a task", member, nil)
	if err != nil || final == "" {
		t.Fatalf("final commit = %q, %v", final, err)
	}
	published, err := h.git.PublishRunBranch(ctx, run)
	if err != nil || published != final {
		t.Fatalf("publish after the final commit = %s, %v", published, err)
	}
	if author := hostGit(t, repo, "log", "-1", "--format=%an <%ae>|%cn|%P", "refs/heads/"+branch); author != "Member <member@localhost>|Aether|"+head {
		t.Fatalf("published final commit: %s", author)
	}
	if files := hostGit(t, repo, "ls-tree", "-r", "--name-only", final); !strings.Contains(files, "after-commit.txt") || !strings.Contains(files, "uploads/blob.bin") {
		t.Fatalf("final commit holds %q", files)
	}
	revision, err := h.git.CaptureEvidence(ctx, run, "packet-"+prefix)
	if err != nil || revision.Tree != hostGit(t, repo, "rev-parse", final+"^{tree}") {
		t.Fatalf("evidence = %+v, %v", revision, err)
	}
	if err = h.e.destroy(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	patch, err = h.git.RunPatch(t.Context(), run, gitengine.PatchRequest{})
	if err != nil || !patch.Recorded || !strings.Contains(patch.Text, "after-commit.txt") {
		t.Fatalf("patch of an ended session = %+v, %v", patch, err)
	}
	if read, err = h.git.FilesRead(ctx, ws, run, "", "after-commit.txt", 0); err != nil || string(read.Content) != "after\n" {
		t.Fatalf("files read of an ended session = %+v, %v", read, err)
	}
	if err := h.git.RemoveRunCheckout(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store, "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("removed checkout's history still borrows the workspace's objects: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.repos), "checkouts", key, "disk.qcow2")); !os.IsNotExist(err) {
		t.Fatalf("removed checkout's disk remains: %v", err)
	}
}

// TestKVMGitServiceBesideAFullSession runs the git service's heaviest work
// while the session holds all the memory its cap leaves: a snapshot and a
// commit of every file of a checkout, then a Files write and read at the
// size limit. The service must come through with no OOM kill. Then it is
// killed during a commit: nothing it started may outlive it, and the run
// must be picked back up.
func TestKVMGitServiceBesideAFullSession(t *testing.T) {
	h := newCheckoutHost(t)
	const files = 40000
	ws := domain.WorkspaceID("wscheckout" + strconv.Itoa(files))
	base := h.workspace(t, ws, files)
	key := fmt.Sprintf("run-vmgit-%x", time.Now().Unix())
	run := domain.RunID(key)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h.session(t, ws, key, base, runtime.Mount{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true})
	free := h.sh(t, key, "echo $(( $(cat /sys/fs/cgroup/session/memory.max) - $(cat /sys/fs/cgroup/session/memory.current) - (32 << 20) ))")
	_, att, err := h.e.spawn(t.Context(), key, guest.ProcessSpec{
		ID: "hold", Argv: []string{"/opt/aether-vmtest", "-test.run", "^TestKVMHoldNow$"}, Env: []string{"AETHER_VM_TEST_HOLD_NOW=" + free},
	})
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, att, "held")
	const gitService = "for p in $(cat /sys/fs/cgroup/git/cgroup.procs); do tr '\\0' ' ' < /proc/$p/cmdline | grep -q git-service && echo $p; done; true"
	service := h.sh(t, key, gitService)
	// Page cache is reclaimable, so only the git cgroup's anonymous memory
	// competes with the helper's reserve: sample its peak while git works.
	h.sh(t, key, `(peak=0; while [ ! -e /tmp/anon-stop ]; do while read -r k v; do [ "$k" = anon ] && [ "$v" -gt "$peak" ] && peak=$v && echo $peak > /tmp/anon-peak; done < /sys/fs/cgroup/git/memory.stat; sleep 0.05; done) >/dev/null 2>&1 &`)
	h.sh(t, key, "for f in src/*/*.txt; do echo changed >> $f; done; head -c 100M /dev/urandom > big.bin")
	start := time.Now()
	// 31 seconds on the KVM test host; CI's nested VMs are slower.
	waitDiff(t, h.diffs, "big.bin", 3*time.Minute)
	snapshot := time.Since(start)
	member := domain.GitIdentity{Name: "Member", Email: "member@localhost"}
	start = time.Now()
	commit, err := h.git.CommitAll(t.Context(), run, "aether: a task", member, nil)
	if err != nil || commit == "" {
		t.Errorf("final commit beside a full session = %q, %v", commit, err)
	}
	committed := time.Since(start)
	content := bytes.Repeat([]byte("0123456789abcde\n"), guest.MaxFileBytes/16)
	start = time.Now()
	if _, err = h.git.FilesWrite(t.Context(), ws, run, "", "upload.txt", content, "", domain.GitIdentity{}, nil); err != nil {
		t.Errorf("upload at the size limit beside a full session: %v", err)
	}
	read, err := h.git.FilesRead(t.Context(), ws, run, "", "upload.txt", 0)
	if err != nil || !bytes.Equal(read.Content, content) {
		t.Errorf("read of that upload: %d bytes, %v", len(read.Content), err)
	}
	transfer := time.Since(start)
	anon := h.sh(t, key, "touch /tmp/anon-stop; sleep 0.2; cat /tmp/anon-peak")
	t.Logf("with the session holding %s bytes: snapshot %s, commit %s, a 64 MiB write and read %s; git's anonymous memory peaked at %s bytes", free, snapshot, committed, transfer, anon)
	if after := h.sh(t, key, gitService+"; grep -h oom_kill /sys/fs/cgroup/git/memory.events /sys/fs/cgroup/session/memory.events"); after != service+"\noom_kill 0\noom_kill 0" {
		t.Errorf("git service %s; afterwards the git service, then OOM kills in its cgroup and the session: %q", service, after)
	}

	// The two big files would keep every later snapshot over its input
	// bound.
	h.sh(t, key, "rm big.bin upload.txt; for f in src/1*/*.txt; do echo again >> $f; done")
	committing := make(chan error, 1)
	go func() {
		_, commitErr := h.git.CommitAll(context.WithoutCancel(t.Context()), run, "aether: again", member, nil)
		committing <- commitErr
	}()
	time.Sleep(time.Second)
	// The file is written while no service runs, so no watch sees it: the
	// next service must record it without another change.
	before := h.sh(t, key, "cat /sys/fs/cgroup/git/cgroup.procs")
	service = h.sh(t, key, gitService)
	killer, _, err := h.e.spawn(t.Context(), key, guest.ProcessSpec{
		ID: "kill", Argv: []string{"/bin/sh", "-c", "kill -9 " + service + " && echo during > /workspace/during-kill.txt"}, User: "0:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, waitErr := killer.Wait(t.Context()); waitErr != nil || code != 0 {
		t.Fatalf("kill the git service: %d, %v", code, waitErr)
	}
	if commitErr := <-committing; commitErr == nil {
		t.Error("the commit outlived the git service it ran in, so the kill missed it")
	}
	deadline := time.Now().Add(time.Minute)
	for ; ; time.Sleep(200 * time.Millisecond) {
		procs := h.sh(t, key, "cat /sys/fs/cgroup/git/cgroup.procs")
		left := false
		for _, pid := range strings.Fields(before) {
			left = left || slices.Contains(strings.Fields(procs), pid)
		}
		next := h.sh(t, key, gitService)
		if !left && next != "" && !strings.Contains(next, "\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a minute after the kill the git cgroup holds %q (before: %q), not one new service and none of what it started", procs, before)
		}
	}
	store := filepath.Join(filepath.Dir(h.repos), "checkouts", key+".diffsnap")
	for deadline = time.Now().Add(3 * time.Minute); ; time.Sleep(time.Second) {
		out, _ := exec.Command("git", "-C", store, "ls-tree", "--name-only", "refs/aether/published", "during-kill.txt").Output()
		if strings.TrimSpace(string(out)) == "during-kill.txt" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the file written while the git service was down never reached the server")
		}
	}
	if commit, err = h.git.CommitAll(t.Context(), run, "aether: after", member, nil); err != nil || commit == "" {
		t.Errorf("commit after the git service was killed mid-commit = %q, %v", commit, err)
	}
}

func waitDiff(t *testing.T, diffs <-chan events.Event, path string, within time.Duration) {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case ev := <-diffs:
			p := ev.Payload.(events.RunDiffPayload)
			for _, f := range p.Files {
				if f.Path == path && p.Tree != "" {
					return
				}
			}
		case <-timeout:
			t.Fatalf("no run.diff with %s", path)
		}
	}
}

func hasEntry(entries []gitengine.TreeEntry, name string) bool {
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}
