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
