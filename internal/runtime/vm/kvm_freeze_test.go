//go:build kvm

package vm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKVMFreezeStopsABurstBeforeTheHostRunsOut has three VMs fill their
// memory at once, 3600 MiB between them at up to 600 MiB/s, on a host this
// test gives 2400 MiB: more than it has, and gone sooner than one VM is
// saved. It checks memory as the scheduler's brake does, every 500 ms, and
// freezes the VMs below a 1200 MiB line, which leaves the burst two seconds
// as admission's floor left the 1.4 GiB/s burst measured on a 29 GiB host.
// The freeze must land with memory to spare and hold it there, a frozen VM
// must save and wake with its fill carrying on where it stopped, and the
// thawed ones must finish theirs.
func TestKVMFreezeStopsABurstBeforeTheHostRunsOut(t *testing.T) {
	e := kvmEngine(t)
	keys := []string{"run-burst-a", "run-burst-b", "run-burst-c"}
	const (
		host, line = 2400 << 20, 1200 << 20
		filled     = "1258291200"
		fill       = "while [ ! -e /dev/shm/go ]; do sleep 0.1; done; i=0; " +
			"while [ $i -lt 30 ]; do head -c 40M /dev/zero >>/dev/shm/fill; i=$((i+1)); sleep 0.2; done; " +
			"touch /dev/shm/done; exec sleep infinity"
	)
	for _, key := range keys {
		t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
		spec := runtime.Spec{
			Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", fill},
			Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 3 << 30,
		}
		if _, err := e.create(t.Context(), key, spec, options{Offline: true}); err != nil {
			t.Fatal(err)
		}
		if err := e.start(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	held := func() uint64 {
		use, err := e.memoryUse()
		if err != nil {
			t.Fatal(err)
		}
		var n uint64
		for _, key := range keys {
			n += use[runtime.ID(idPrefix+key)]
		}
		return n
	}
	base := held()
	taken := func() uint64 { return max(held(), base) - base }
	for _, key := range keys {
		guestFields(t, e, key, "touch /dev/shm/go", 0)
	}
	started := time.Now()
	for taken() <= host-line {
		if time.Since(started) > time.Minute {
			t.Fatalf("after a minute the VMs took %d MiB, never the %d MiB that leaves the host under its line", taken()>>20, (host-line)>>20)
		}
		time.Sleep(500 * time.Millisecond)
	}
	rate := float64(taken()>>20) / time.Since(started).Seconds()
	for _, key := range keys {
		if froze, err := e.freeze(key); !froze || err != nil {
			t.Fatalf("freeze %s = %v, %v", key, froze, err)
		}
	}
	// A vCPU finishes the page fault it is in before it freezes.
	time.Sleep(time.Second)
	landed := taken()
	if landed >= host {
		t.Fatalf("the freeze landed with the VMs holding %d MiB of a %d MiB host", landed>>20, host>>20)
	}
	time.Sleep(5 * time.Second)
	if after := taken(); after > landed+32<<20 {
		t.Fatalf("frozen VMs took %d MiB more in 5 s", (after-landed)>>20)
	}
	if frozen := e.frozenVMs(); len(frozen) != len(keys) {
		t.Fatalf("frozen = %v, want the three VMs", frozen)
	}

	saving := time.Now()
	if err := e.save(t.Context(), keys[0]); err != nil {
		t.Fatalf("save a frozen VM: %v", err)
	}
	saveTook := time.Since(saving)
	saved, err := e.savedMemory(keys[0])
	if err != nil || saved == 0 {
		t.Fatalf("the saved VM holds %d bytes on disk, %v", saved, err)
	}
	if after := taken(); after > landed {
		t.Fatalf("the VMs hold %d MiB after one was saved, %d MiB before", after>>20, landed>>20)
	}
	t.Logf("the burst took %.0f MiB/s; frozen, the VMs held %d MiB of the %d MiB host, and the burst needed %.1f s for the rest; saving one VM (%d MiB) took %s",
		rate, landed>>20, host>>20, float64((host-landed)>>20)/rate, saved>>20, saveTook.Round(time.Millisecond))

	for _, key := range keys[1:] {
		if err := e.thaw(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.restore(t.Context(), keys[0]); err != nil {
		t.Fatalf("wake the VM saved frozen: %v", err)
	}
	for _, key := range keys {
		for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
			out, code := vmRun(t, e, key, "test -e /dev/shm/done && stat -c %s /dev/shm/fill")
			if code == 0 && strings.TrimSpace(out) == filled {
				break
			}
			if code == 0 || time.Now().After(deadline) {
				t.Fatalf("%s running again: its fill is at %q (exit %d), want %s bytes within 2 minutes", key, out, code, filled)
			}
		}
	}
}
