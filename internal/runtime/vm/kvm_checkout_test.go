//go:build kvm

package vm

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		CheckoutDisks: func(ctx context.Context, path string, uid, gid int) (string, guest.Checkout, bool, error) {
			return h.git.VMCheckoutDisk(ctx, path, uid, gid)
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

func (h *checkoutHost) session(t *testing.T, ws domain.WorkspaceID, key, base string) time.Duration {
	t.Helper()
	t.Cleanup(func() { _ = h.e.destroy(context.Background(), key) })
	t.Cleanup(func() { _ = h.git.RemoveRunHistory(context.Background(), domain.RunID(key)) })
	checkout, _, err := h.git.CreateRunCheckoutAt(t.Context(), ws, domain.RunID(key), base, "main", "a task", "")
	if err != nil {
		t.Fatal(err)
	}
	created := h.boot(t, key, checkout, "1000:1000")
	if err := h.git.StartDiffWatch(t.Context(), ws, domain.RunID(key)); err != nil {
		t.Fatal(err)
	}
	return created
}

// boot creates and starts the VM of the session key as user, and returns
// how long the create took.
func (h *checkoutHost) boot(t *testing.T, key, checkout, user string) time.Duration {
	t.Helper()
	spec := runtime.Spec{
		Image: cmp.Or(os.Getenv("AETHER_TEST_IMAGE"), checkoutImage), CreationKey: key, TTY: true, User: user,
		Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env:     map[string]string{"AETHER_RUN_ID": key, "HOME": "/home/aether"}, CPULimit: 2, MemoryLimitBytes: 1 << 30,
		WorktreeHostPath: checkout, WorktreeMountPath: "/workspace", WorkingDir: "/workspace",
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
	waitDiff(t, h.diffs, "new.txt")
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

	h.sh(t, key, "echo after > after-commit.txt")
	if err = h.e.stop(t.Context(), key, time.Second); err != nil {
		t.Fatal(err)
	}
	if published, publishErr := h.git.PublishRunBranch(t.Context(), run); publishErr != nil || published != head {
		t.Fatalf("publish after the main process exited = %s, %v", published, publishErr)
	}
	h.git.StopDiffWatch(run)
	store := filepath.Join(filepath.Dir(h.repos), "checkouts", key+".diffsnap")
	if got := hostGit(t, store, "ls-tree", "--name-only", "refs/aether/published", "after-commit.txt"); got != "after-commit.txt" {
		t.Fatalf("the last snapshot, taken after the main process exited, lacks the last file: %q", got)
	}
	if err = h.e.destroy(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	patch, err = h.git.RunPatch(t.Context(), run, gitengine.PatchRequest{})
	if err != nil || !patch.Recorded || !strings.Contains(patch.Text, "after-commit.txt") {
		t.Fatalf("patch of an ended session = %+v, %v", patch, err)
	}
}

func waitDiff(t *testing.T, diffs <-chan events.Event, path string) {
	t.Helper()
	timeout := time.After(60 * time.Second)
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
