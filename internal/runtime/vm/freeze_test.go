package vm

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestFreezeTakesARunningVMThatIsNotMidOperation(t *testing.T) {
	h := newHarness(t)
	for _, key := range []string{"run-a", "run-b", "run-c"} {
		h.createStarted(key, sessionSpec(key))
	}
	if err := h.engine.stop(t.Context(), "run-c", 0); err != nil {
		t.Fatal(err)
	}
	if froze, err := h.engine.freeze("run-c"); froze || err == nil {
		t.Fatalf("freeze of a stopped VM = %v, %v", froze, err)
	}
	unlock := h.engine.lock("run-b")
	if froze, err := h.engine.freeze("run-b"); froze || err == nil {
		t.Fatalf("freeze of a VM mid-operation = %v, %v", froze, err)
	}
	unlock()
	for _, key := range []string{"run-a", "run-b"} {
		if froze, err := h.engine.freeze(key); !froze || err != nil {
			t.Fatalf("freeze %s = %v, %v", key, froze, err)
		}
	}
	if froze, err := h.engine.freeze("run-a"); froze || err != nil {
		t.Fatalf("freeze of a frozen VM = %v, %v", froze, err)
	}
	if data, err := os.ReadFile(h.engine.freezeFile("run-a")); err != nil || strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("the freezer of a frozen VM reads %q, %v", data, err)
	}
	if frozen := h.engine.frozenVMs(); !slices.Equal(frozen, []runtime.ID{idPrefix + "run-a", idPrefix + "run-b"}) {
		t.Fatalf("frozen = %v, want the longest frozen first", frozen)
	}
	if err := h.engine.thaw("run-a"); err != nil {
		t.Fatal(err)
	}
	// A new engine, as after a server restart, still finds what is frozen.
	if frozen := h.newEngine(h.engine.cfg.DataDir).frozenVMs(); !slices.Equal(frozen, []runtime.ID{idPrefix + "run-b"}) {
		t.Fatalf("after a thaw and a restart frozen = %v", frozen)
	}
	if err := h.engine.stop(t.Context(), "run-b", 0); err != nil {
		t.Fatalf("stop a frozen VM: %v", err)
	}
}

func TestSaveOfAFrozenVMPausesItWithoutLettingItsGuestRun(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	vmm := h.units.vmm("run-a")
	if froze, err := h.engine.freeze("run-a"); err != nil || !froze {
		t.Fatalf("freeze = %v, %v", froze, err)
	}
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(vmm.called(), []string{"vm.info", "vm.pause", "vm.snapshot", "vmm.shutdown"}) {
		t.Errorf("save calls = %v", vmm.called())
	}
	if g := h.guest("run-a"); g.syncs != 0 {
		t.Errorf("the save synced a frozen guest %d times; a sync lets it run", g.syncs)
	}
	if err := h.engine.restore(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if h.engine.frozen("run-a") {
		t.Error("the restored VM is frozen")
	}
}

func TestAFailedSaveLeavesTheVMRunning(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	vmm := h.units.vmm("run-a")
	vmm.mu.Lock()
	vmm.snapshotFails = true
	vmm.mu.Unlock()
	if err := h.engine.save(t.Context(), "run-a"); err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("save = %v, want the snapshot's error", err)
	}
	if h.engine.halted(t.Context(), "run-a") {
		t.Fatal("a failed save left the VM paused")
	}
}

// A snapshot runs to its end however long it takes: cut short, the monitor
// would go on writing it with the VM paused.
func TestASnapshotOutlastsTheSavesContext(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	vmm := h.units.vmm("run-a")
	hold := make(chan struct{})
	vmm.mu.Lock()
	vmm.snapshotHold = hold
	vmm.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	saved := make(chan error, 1)
	go func() { saved <- h.engine.save(ctx, "run-a") }()
	<-hold
	cancel()
	hold <- struct{}{}
	if err := <-saved; err != nil {
		t.Fatalf("a save whose context ended during the snapshot = %v", err)
	}
	if rec, err := h.engine.load("run-a"); err != nil || !rec.Saved {
		t.Fatalf("after the snapshot the record is %+v, %v; want it saved", rec, err)
	}
}
