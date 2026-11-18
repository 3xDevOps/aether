//go:build kvm

package vm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKVMBootForCaptureCommitsAStoppedVMsWork ends a session VM before its
// final commit, as the host OOM killer does, then boots it for capture: the
// checkout is served again, neither the command nor the setup script runs,
// and the commit holds the work. The next start runs the command again; the
// setup script ran once, at the first boot, as it does for every VM.
func TestKVMBootForCaptureCommitsAStoppedVMsWork(t *testing.T) {
	h := newCheckoutHost(t)
	ws := domain.WorkspaceID("wscapture")
	base := h.workspace(t, ws, 10)
	key := fmt.Sprintf("run-vmcap-%x", time.Now().Unix())
	run := domain.RunID(key)
	t.Cleanup(func() { _ = h.e.destroy(context.Background(), key) })
	t.Cleanup(func() { _ = h.git.RemoveRunHistory(context.Background(), run) })
	checkout, _, err := h.git.CreateRunCheckoutAt(t.Context(), ws, run, base, "main", "a task", "")
	if err != nil {
		t.Fatal(err)
	}
	spec := runtime.Spec{
		Image: cmp.Or(os.Getenv("AETHER_TEST_IMAGE"), checkoutImage), CreationKey: key, TTY: true, User: "1000:1000",
		Command:     []string{"/bin/sh", "-c", "echo main >> /workspace/ran; exec sleep infinity"},
		SetupScript: "echo setup >> /workspace/ran",
		Env:         map[string]string{"AETHER_RUN_ID": key, "HOME": "/home/aether"}, CPULimit: 1, MemoryLimitBytes: 1 << 30,
		WorktreeHostPath: checkout, WorktreeMountPath: "/workspace", WorkingDir: "/workspace",
	}
	ctx := t.Context()
	if _, err = h.e.create(ctx, key, spec, options{}); err != nil {
		t.Fatal(err)
	}
	if err = h.e.start(ctx, key); err != nil {
		t.Fatal(err)
	}
	waitRan := func(want string) {
		t.Helper()
		h.sh(t, key, fmt.Sprintf(`for i in $(seq 100); do [ "$(cat ran)" = '%s' ] && exit 0; sleep 0.1; done; cat ran; exit 1`, want))
	}
	waitRan("setup\nmain")
	// A kill loses what the guest had not yet written to its disks.
	h.sh(t, key, "echo work > work.txt && sync")

	h.e.kill(ctx, key)
	member := domain.GitIdentity{Name: "Member", Email: "member@localhost"}
	if _, err = h.git.CommitAll(ctx, run, "wip: a task", member, nil); !errors.Is(err, guest.ErrVMNotRunning) {
		t.Fatalf("commit of a VM that stopped = %v, want ErrVMNotRunning", err)
	}
	start := time.Now()
	if err = (&Runtime{vms: h.e}).BootForCapture(ctx, runtime.ID(idPrefix+key)); err != nil {
		t.Fatal(err)
	}
	t.Logf("boot for capture took %s", time.Since(start))
	commit, err := h.git.CommitAll(ctx, run, "wip: a task", member, nil)
	if err != nil || commit == "" {
		t.Fatalf("commit of a VM booted for capture = %q, %v", commit, err)
	}
	store := filepath.Join(filepath.Dir(h.repos), "checkouts", key+".diffsnap")
	if got := hostGit(t, store, "show", commit+":work.txt"); got != "work" {
		t.Errorf("the commit holds work.txt %q", got)
	}
	if got := hostGit(t, store, "show", commit+":ran"); got != "setup\nmain" {
		t.Errorf("the boot for capture ran the command or setup script: ran is %q", got)
	}
	if err = h.e.stop(ctx, key, time.Second); err != nil {
		t.Fatal(err)
	}

	if err = h.e.start(ctx, key); err != nil {
		t.Fatal(err)
	}
	waitRan("setup\nmain\nmain")
}
