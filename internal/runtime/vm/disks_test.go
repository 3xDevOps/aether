package vm

import (
	"bytes"
	"os"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestDiskUseCountsTheBlocksOfEachVMsDisks(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	h.createStarted("run-b", sessionSpec("run-b"))
	for _, name := range []string{rootDisk, dataDisk} {
		if err := os.WriteFile(h.engine.path("run-a", name), bytes.Repeat([]byte{1}, 1<<20), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	use, err := h.engine.diskUse()
	if err != nil {
		t.Fatal(err)
	}
	if a, b := use[runtime.ID(idPrefix+"run-a")], use[runtime.ID(idPrefix+"run-b")]; a < 2<<20 || b >= 1<<20 || len(use) != 2 {
		t.Errorf("disk use = %v; want 2 MiB for run-a's root and data disks and nothing for run-b", use)
	}
}

func TestHoldDisksStopsAFrozenVMUntilItsResume(t *testing.T) {
	h := newHarness(t)
	h.createWithCheckoutDisk("run-b")
	if err := h.engine.pause(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.holdDisks(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if !h.engine.halted(t.Context(), "run-b") || !h.guest("run-b").isSessionFrozen() {
		t.Fatal("a held VM runs on, or its session thawed")
	}
	if err := h.engine.holdDisks(t.Context(), "run-b"); err != nil {
		t.Errorf("holding a held VM again = %v", err)
	}
	if err := h.engine.resume(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if h.engine.halted(t.Context(), "run-b") || h.guest("run-b").isSessionFrozen() {
		t.Error("resume left the held VM stopped or its session frozen")
	}
}

// A VM the disk guard holds still runs, so a capture only starts its vCPUs:
// no second boot, and its session stays frozen.
func TestBootForCaptureStartsTheVCPUsOfAHeldVM(t *testing.T) {
	h := newHarness(t)
	h.createWithCheckoutDisk("run-b")
	if err := h.engine.pause(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.holdDisks(t.Context(), "run-b"); err != nil {
		t.Fatal(err)
	}
	g := h.guest("run-b")
	boots := len(g.boots)
	if err := (&Runtime{vms: h.engine}).BootForCapture(t.Context(), runtime.ID(idPrefix+"run-b")); err != nil {
		t.Fatal(err)
	}
	if h.engine.halted(t.Context(), "run-b") || !g.isSessionFrozen() || len(g.boots) != boots {
		t.Errorf("after the capture boot: halted %v, frozen %v, boots %d, want %d", h.engine.halted(t.Context(), "run-b"), g.isSessionFrozen(), len(g.boots), boots)
	}
}

// A saved VM is restored for capture, not started from its disks: its saved
// memory may hold writes its disks do not. Its session stays frozen, so the
// agent a sleep saved running does not work on.
func TestBootForCaptureRestoresASavedVM(t *testing.T) {
	h := newHarness(t)
	h.createWithCheckoutDisk("run-a")
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	g := h.guest("run-a")
	boots := len(g.boots)
	if err := (&Runtime{vms: h.engine}).BootForCapture(t.Context(), runtime.ID(idPrefix+"run-a")); err != nil {
		t.Fatal(err)
	}
	rec, err := h.engine.load("run-a")
	if err != nil || rec.Saved || len(g.boots) != boots || !g.isSessionFrozen() {
		t.Errorf("after the capture boot: %v, saved %v, boots %d (want %d), frozen %v; want the VM restored with its session frozen",
			err, rec != nil && rec.Saved, len(g.boots), boots, g.isSessionFrozen())
	}
}
