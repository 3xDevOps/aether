package vm

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

func terminalSpec() runtime.Spec {
	return runtime.Spec{
		Image: "aether/member-m1:a", Command: []string{"/bin/sh", "-l"}, TTY: true, NestedDocker: true,
		Env: map[string]string{"HOME": "/root"}, CreationKey: "terminal:m1", User: "1000:1000",
	}
}

func (h *harness) startTerminal() {
	h.t.Helper()
	name, opts, ok := vmFor(terminalSpec())
	if !ok || name != "terminal-m1" || !opts.KeepRoot {
		h.t.Fatalf("vmFor(terminal) = %q, %+v, %v", name, opts, ok)
	}
	if _, err := h.engine.create(h.t.Context(), name, terminalSpec(), opts); err != nil {
		h.t.Fatal(err)
	}
	if err := h.engine.start(h.t.Context(), name); err != nil {
		h.t.Fatal(err)
	}
}

func TestTerminalKeepsItsRootDiskAcrossStopAndStart(t *testing.T) {
	h := newHarness(t)
	h.startTerminal()
	if err := os.WriteFile(h.engine.path("terminal-m1", rootDisk), []byte("installed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.stop(t.Context(), "terminal-m1", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.start(t.Context(), "terminal-m1"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(h.engine.path("terminal-m1", rootDisk)); string(data) != "installed" || h.images.roots() != 1 {
		t.Fatalf("root disk %q after %d root disks; a terminal keeps its own", data, h.images.roots())
	}
	if stats, err := h.engine.stats(t.Context()); err != nil || len(stats) != 1 || stats[0].CreationKey != "terminal:m1" {
		t.Errorf("stats = %+v, %v; want the terminal's own creation key, which the System view reads its owner from", stats, err)
	}
	r := &Runtime{vms: h.engine}
	if id, err := r.FindByCreationKey(t.Context(), "terminal:m1"); err != nil || id != "vm-terminal-m1" {
		t.Fatalf("FindByCreationKey = %q, %v", id, err)
	}
	if _, err := r.FindByCreationKey(t.Context(), "terminal:m2"); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("FindByCreationKey of another member = %v", err)
	}
}

func TestKeptVMTheHostCannotStartSaysStartLater(t *testing.T) {
	h := newHarness(t)
	h.startTerminal()
	if err := h.engine.stop(t.Context(), "terminal-m1", time.Second); err != nil {
		t.Fatal(err)
	}
	h.units.failNext = true
	g := h.guest("terminal-m1")
	g.mu.Lock()
	g.pingErr = errors.New("connection refused")
	g.mu.Unlock()
	err := h.engine.startKept(t.Context(), "terminal-m1")
	if !errors.Is(err, runtime.ErrStartLater) {
		t.Fatalf("a kept VM whose monitor did not run = %v", err)
	}
	g.mu.Lock()
	g.pingErr = nil
	g.bootError = "switch to the root disk: mount /dev/vdb at /mnt: structure needs cleaning"
	g.mu.Unlock()
	err = h.engine.startKept(t.Context(), "terminal-m1")
	if err == nil || errors.Is(err, runtime.ErrStartLater) || !errors.Is(err, runtime.ErrUnstartableImage) {
		t.Fatalf("a kept VM whose disk does not mount = %v; it is the disk's fault", err)
	}
	if _, statErr := os.Stat(h.engine.path("terminal-m1", rootDisk)); statErr != nil {
		t.Errorf("a failed start lost the kept root disk: %v", statErr)
	}
}

// copyStep runs the engine's cp for real and records what the VM looked
// like while it ran.
type copyStep struct {
	mu      sync.Mutex
	h       *harness
	fail    bool
	state   string
	freezes []string
}

func (c *copyStep) run(_ context.Context, name string, args ...string) error {
	if name != "cp" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if vmm := c.h.units.vmm("terminal-m1"); vmm != nil {
		vmm.mu.Lock()
		c.state = vmm.state
		vmm.mu.Unlock()
	}
	c.freezes = c.h.guest("terminal-m1").frozen()
	if c.fail {
		return errors.New("cp: No space left on device")
	}
	data, err := os.ReadFile(args[len(args)-2])
	if err != nil {
		return err
	}
	return os.WriteFile(args[len(args)-1], data, 0o600)
}

func TestCommitCopiesTheRootDiskWhileFrozen(t *testing.T) {
	h := newHarness(t)
	h.startTerminal()
	if err := os.WriteFile(h.engine.path("terminal-m1", rootDisk), []byte("installed"), 0o600); err != nil {
		t.Fatal(err)
	}
	cp := &copyStep{h: h}
	h.engine.run = cp.run
	labels := map[string]string{"aether.previous-image": "aether/member-m1:a"}
	if err := h.engine.commit(t.Context(), "terminal-m1", "aether/pending-member-m1:b", labels); err != nil {
		t.Fatal(err)
	}
	if cp.state != "Running" || !slices.Equal(cp.freezes, []string{"freeze " + freezeTimeout.String()}) {
		t.Errorf("the copy ran with the VM %s and freezes %v", cp.state, cp.freezes)
	}
	if got := h.guest("terminal-m1").frozen(); !slices.Equal(got, []string{"freeze " + freezeTimeout.String(), "thaw"}) {
		t.Errorf("freezes = %v", got)
	}
	if calls := h.units.vmm("terminal-m1").called(); slices.Contains(calls, "vm.pause") {
		t.Errorf("the save paused the VM, which a crash would leave paused: %v", calls)
	}
	if len(h.images.saves) != 1 {
		t.Fatalf("saves = %+v", h.images.saves)
	}
	save := h.images.saves[0]
	want := vmimage.Disk{Path: h.engine.path("terminal-m1", rootCopy), Format: vmimage.FormatQcow2}
	if save.disk != want || save.base.ID != "sha256:b" || save.copied != "installed" || save.ref != "aether/pending-member-m1:b" || save.labels["aether.previous-image"] != "aether/member-m1:a" {
		t.Errorf("save = %+v", save)
	}
	if _, err := os.Stat(save.disk.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy was left behind: %v", err)
	}
}

func TestCommitThawsWhenTheCopyFails(t *testing.T) {
	h := newHarness(t)
	h.startTerminal()
	h.engine.run = (&copyStep{h: h, fail: true}).run
	err := h.engine.commit(t.Context(), "terminal-m1", "aether/pending-member-m1:b", nil)
	if err == nil || !strings.Contains(err.Error(), "No space left") {
		t.Fatalf("commit = %v", err)
	}
	if got := h.guest("terminal-m1").frozen(); len(got) != 2 || got[1] != "thaw" {
		t.Errorf("freezes = %v", got)
	}
	if len(h.images.saves) != 0 {
		t.Errorf("saves %+v", h.images.saves)
	}
}

func TestCommitRefusesACopyThatOutlastedTheFreeze(t *testing.T) {
	h := newHarness(t)
	h.startTerminal()
	h.engine.run = (&copyStep{h: h}).run
	h.guest("terminal-m1").thawed = true
	err := h.engine.commit(t.Context(), "terminal-m1", "aether/pending-member-m1:b", nil)
	if !errors.Is(err, errTooSlow) {
		t.Fatalf("commit of a copy the deadline cut = %v", err)
	}
	if len(h.images.saves) != 0 {
		t.Errorf("an inconsistent copy was saved: %+v", h.images.saves)
	}
}

func TestCommitThawsAFreezeWhoseReplyFailed(t *testing.T) {
	h := newHarness(t)
	h.startTerminal()
	h.guest("terminal-m1").freezeErr = context.DeadlineExceeded
	if err := h.engine.commit(t.Context(), "terminal-m1", "aether/pending-member-m1:b", nil); err == nil {
		t.Fatal("commit succeeded")
	}
	if got := h.guest("terminal-m1").frozen(); len(got) != 2 || got[1] != "thaw" {
		t.Errorf("freezes = %v", got)
	}
	if len(h.images.saves) != 0 {
		t.Errorf("saves %+v", h.images.saves)
	}
}

func TestCommitRefusesAPausedVMAndSavesAStoppedOne(t *testing.T) {
	h := newHarness(t)
	h.startTerminal()
	if err := h.engine.pause(t.Context(), "terminal-m1"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.commit(t.Context(), "terminal-m1", "aether/pending-member-m1:b", nil); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("commit of a paused VM = %v", err)
	}
	if len(h.guest("terminal-m1").frozen()) != 0 {
		t.Error("a paused VM was asked to freeze")
	}
	if err := h.engine.stop(t.Context(), "terminal-m1", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.engine.path("terminal-m1", rootDisk), []byte("stopped"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.engine.run = (&copyStep{h: h}).run
	if err := h.engine.commit(t.Context(), "terminal-m1", "aether/pending-member-m1:b", nil); err != nil {
		t.Fatal(err)
	}
	if len(h.images.saves) != 1 || h.images.saves[0].disk.Path != h.engine.path("terminal-m1", rootCopy) || h.images.saves[0].copied != "stopped" {
		t.Errorf("saves = %+v; want a copy of the stopped root disk, which SaveBase takes", h.images.saves)
	}
}

func TestOfflineVMHasNoNetworkAndALifetime(t *testing.T) {
	h := newHarness(t)
	spec := sessionSpec("check-a")
	if _, err := h.engine.create(t.Context(), "check-a", spec, options{Offline: true, Lifetime: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.start(t.Context(), "check-a"); err != nil {
		t.Fatal(err)
	}
	last := h.units.last()
	if slices.Contains(last.launch.Args, "--net") || strings.Contains(strings.Join(last.launch.Args, " "), "aether.net=") {
		t.Errorf("an offline VM has a network: %v", last.launch.Args)
	}
	if !slices.Contains(last.properties, "RuntimeMaxSec=600") {
		t.Errorf("properties = %v", last.properties)
	}
	if _, err := h.engine.containerIP(t.Context(), "check-a"); err == nil {
		t.Error("an offline VM has an address")
	}
	if index, err := h.engine.allocateNet(); err != nil || index != 1 {
		t.Errorf("an offline VM holds a network: %d, %v", index, err)
	}
}

func TestCheckImageBootsAShellAndRemovesItsVM(t *testing.T) {
	h := newHarness(t)
	r := &Runtime{vms: h.engine}
	if err := r.CheckImage(t.Context(), "aether/pending-member-m1:b", "1000:1000"); err != nil {
		t.Fatal(err)
	}
	for key, g := range h.guests {
		if strings.HasPrefix(key, "check-") && g.boots[0].Main.Dir != "/" {
			t.Errorf("the check's shell runs in %q, the image's own directory, which may be the unmounted home", g.boots[0].Main.Dir)
		}
	}
	if keys, _ := h.engine.keys(); len(keys) != 0 {
		t.Errorf("the check left VMs %v", keys)
	}
	for _, g := range h.guests {
		g.mu.Lock()
		g.bootError = "switch to the root disk: mount /dev/vdb at /mnt: structure needs cleaning"
		g.mu.Unlock()
	}
	err := r.CheckImage(t.Context(), "aether/pending-member-m1:c", "1000:1000")
	if !errors.Is(err, runtime.ErrUnstartableImage) || !strings.Contains(err.Error(), "structure needs cleaning") {
		t.Fatalf("check of an unbootable image = %v", err)
	}
	if keys, _ := h.engine.keys(); len(keys) != 0 {
		t.Errorf("the failed check left VMs %v", keys)
	}
	h.units.failNext = true
	for _, g := range h.guests {
		g.mu.Lock()
		g.bootError, g.pingErr = "", errors.New("connection refused")
		g.mu.Unlock()
	}
	err = r.CheckImage(t.Context(), "aether/pending-member-m1:d", "1000:1000")
	if err == nil || errors.Is(err, runtime.ErrUnstartableImage) {
		t.Fatalf("a check the host could not start = %v; it must not blame the image", err)
	}
}

func TestImagesTheStoreHoldsNeverReachDocker(t *testing.T) {
	h := newHarness(t)
	h.images.tags = map[string]vmimage.Base{"aether/member-m1:a": {ID: "local:1", Config: vmimage.ImageConfig{Labels: map[string]string{"k": "v"}}}}
	r := &Runtime{vms: h.engine}
	if ok, err := r.ImageExists(t.Context(), "aether/member-m1:a"); !ok || err != nil {
		t.Errorf("ImageExists = %v, %v", ok, err)
	}
	if labels, err := r.ImageLabels(t.Context(), "aether/member-m1:a"); err != nil || labels["k"] != "v" {
		t.Errorf("ImageLabels = %v, %v", labels, err)
	}
	if err := r.TagImage(t.Context(), "aether/member-m1:a", "aether/member-m1:b"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.images.LookupBase("aether/member-m1:b"); err != nil {
		t.Errorf("TagImage did not tag in the store: %v", err)
	}
}
