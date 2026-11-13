package vm

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

const testBrowserImage = "ghcr.io/3xdevops/aether-browser:v1"

func (h *harness) browserSession(key string) {
	h.t.Helper()
	h.engine.cfg.BrowserImage = testBrowserImage
	h.images.mu.Lock()
	h.images.tagLocked(testBrowserImage, vmimage.Base{ID: "sha256:browser", Path: "/images/browser.raw", Config: vmimage.ImageConfig{Env: []string{"PATH=/usr/local/bin:/usr/bin", "NODE_VERSION=24"}}})
	h.images.mu.Unlock()
	spec := sessionSpec(key)
	name, opts, _ := vmFor(spec)
	if _, err := h.engine.create(h.t.Context(), name, spec, opts); err != nil {
		h.t.Fatal(err)
	}
	if err := h.engine.start(h.t.Context(), name); err != nil {
		h.t.Fatal(err)
	}
}

func browserSpec(t *testing.T, key string) runtime.BrowserSpec {
	control := t.TempDir()
	return runtime.BrowserSpec{
		RunContainer: runtime.ID(idPrefix + key), Image: testBrowserImage, CreationKey: "browser-0123", Name: "browser-0123",
		ControlHostPath: control, CPULimit: 1.5, MemoryLimitBytes: 1 << 30,
	}
}

func TestSessionVMBootsWithTheBrowsersDiskAndSocket(t *testing.T) {
	h := newHarness(t)
	h.browserSession("run-a")
	last := h.units.last()
	args := strings.Join(last.launch.Args, " ")
	if !strings.Contains(args, "id=browser,serial=browser,backing_files=on,readonly=on") || !strings.Contains(args, "aether.serve=2001,") {
		t.Errorf("a session VM boots without the browser's disk or socket: %s", args)
	}
	h.engine.cfg.BrowserImage = "ghcr.io/3xdevops/aether-browser:v2"
	spec := sessionSpec("run-b")
	if _, err := h.engine.create(t.Context(), "run-b", spec, options{Browser: true}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := h.engine.load("run-b"); rec.Browser.Path != "" {
		t.Errorf("a VM got a browser disk whose image was not built: %+v", rec.Browser)
	}
	newer := browserSpec(t, "run-a")
	newer.Image = h.engine.cfg.BrowserImage
	if _, err := h.engine.createBrowser(t.Context(), "run-a", newer); err == nil || !strings.Contains(err.Error(), "restart the session") {
		t.Errorf("a browser of another image = %v", err)
	}
	if _, err := h.engine.createBrowser(t.Context(), "run-b", browserSpec(t, "run-b")); err == nil {
		t.Error("a browser in a VM without its disk")
	}
}

func TestBrowserRunsInTheSessionVM(t *testing.T) {
	h := newHarness(t)
	h.browserSession("run-a")
	h.engine.cfg.BrowserImage = testBrowserImage
	spec := browserSpec(t, "run-a")
	r := &Runtime{vms: h.engine}
	id, err := r.CreateBrowser(t.Context(), spec)
	if err != nil || id != "vmb-run-a" {
		t.Fatalf("CreateBrowser = %q, %v", id, err)
	}
	if found, findErr := r.vms.findBrowser(spec.CreationKey); findErr != nil || found != id {
		t.Errorf("findBrowser = %q, %v", found, findErr)
	}
	info, err := r.InspectBrowser(t.Context(), id)
	if err != nil || info.State != "created" || info.RunContainer != "vm-run-a" || info.CreationKey != spec.CreationKey {
		t.Fatalf("inspect a created browser = %+v, %v", info, err)
	}
	if err = r.Start(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	g := h.guest("run-a")
	p := g.browserSpec.Process
	if p.User != browserUser || !p.NoNewPrivileges || p.Argv[0] != browserProgram || !slices.Contains(p.Env, "AETHER_BROWSER_CREATION_KEY=browser-0123") || !slices.Contains(p.Env, "NODE_VERSION=24") {
		t.Errorf("companion process = %+v", p)
	}
	if g.browserSpec.MemoryBytes != 1<<30 || g.browserSpec.CPUMillis != 1500 || g.browserSpec.ShmBytes != uint64(runtime.BrowserSharedMemoryBytes) {
		t.Errorf("companion limits = %+v", g.browserSpec)
	}
	if info, err = r.InspectBrowser(t.Context(), id); err != nil || info.State != "running" {
		t.Errorf("inspect a running browser = %+v, %v", info, err)
	}
	if err = r.Pause(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if info, _ = r.InspectBrowser(t.Context(), id); info.State != "paused" {
		t.Errorf("state after pause = %s", info.State)
	}
	if err = r.Resume(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err = r.Destroy(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.browserCalls, []string{"start", "pause true", "pause false", "stop"}) {
		t.Errorf("helper calls = %v", g.browserCalls)
	}
	if _, err = r.InspectBrowser(t.Context(), id); !errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("inspect a destroyed browser = %v", err)
	}
	if err = r.Destroy(t.Context(), id); err != nil {
		t.Errorf("a second destroy = %v", err)
	}
	if err = h.engine.destroy(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if err = r.Destroy(t.Context(), id); err != nil {
		t.Errorf("destroy after the VM went = %v", err)
	}
}

func TestBrowserOfAStoppedSessionReadsExited(t *testing.T) {
	h := newHarness(t)
	h.browserSession("run-a")
	r := &Runtime{vms: h.engine}
	id, err := r.CreateBrowser(t.Context(), browserSpec(t, "run-a"))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	g := h.guest("run-a")
	g.mu.Lock()
	g.procs[g.browser].exit(1)
	g.mu.Unlock()
	if info, _ := r.InspectBrowser(t.Context(), id); info.State != "exited" {
		t.Errorf("a companion that exited reads %s", info.State)
	}
	if err = h.engine.stop(t.Context(), "run-a", 0); err != nil {
		t.Fatal(err)
	}
	if info, _ := r.InspectBrowser(t.Context(), id); info.State != "exited" {
		t.Errorf("the browser of a stopped VM reads %s", info.State)
	}
}
