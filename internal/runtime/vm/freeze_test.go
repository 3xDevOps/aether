package vm

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestFreezeAllFreezesEveryRunningVMButOneMidOperation(t *testing.T) {
	h := newHarness(t)
	for _, key := range []string{"run-a", "run-b", "run-c"} {
		h.createStarted(key, sessionSpec(key))
	}
	if err := h.engine.stop(t.Context(), "run-c", 0); err != nil {
		t.Fatal(err)
	}

	unlock := h.engine.lock("run-b")
	if froze := h.engine.freezeAll(); !slices.Equal(froze, []runtime.ID{idPrefix + "run-a"}) {
		t.Fatalf("with run-b mid-operation and run-c stopped, freezeAll froze %v", froze)
	}
	unlock()
	if froze := h.engine.freezeAll(); !slices.Equal(froze, []runtime.ID{idPrefix + "run-b"}) {
		t.Fatalf("the next freezeAll froze %v, want the VM the first left out", froze)
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

func TestAFailedSaveLeavesTheVMRunningOrStopsIt(t *testing.T) {
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

	// A monitor that answers no resume holds a VM nothing can run again.
	vmm.mu.Lock()
	vmm.stuck = true
	vmm.mu.Unlock()
	err := h.engine.save(t.Context(), "run-a")
	if err == nil || !strings.Contains(err.Error(), "the VM was stopped") {
		t.Fatalf("save with a monitor that does not resume = %v", err)
	}
	if info, err := h.engine.inspect(t.Context(), "run-a"); err != nil || !info.Exited {
		t.Fatalf("a VM left paused by a failed save inspects as %+v, %v; want it stopped", info, err)
	}
	if rec, _ := h.engine.load("run-a"); rec.Saved {
		t.Error("the stopped VM counts as saved")
	}
}
