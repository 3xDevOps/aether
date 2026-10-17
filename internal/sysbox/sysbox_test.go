package sysbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestMain(m *testing.M) {
	systemdRun = ""
	os.Exit(m.Run())
}

// The fake systemd-run honors the two properties runInUnit sets and runs the
// script as the unit would: output appended to the log, status returned.
func TestRunScriptInUnit(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "systemd-run")
	args := filepath.Join(t.TempDir(), "args")
	body := "#!/bin/sh\necho \"$*\" >" + args + "\nlog=\nwhile [ $# -gt 0 ]; do case \"$1\" in StandardOutput=append:*) log=${1#*:} ;; /bin/sh) shift; exec /bin/sh \"$@\" >>\"$log\" 2>&1 ;; esac; shift; done\n"
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	systemdRun = fake
	t.Cleanup(func() { systemdRun = "" })

	var out bytes.Buffer
	var unsupported *UnsupportedError
	err := runScript(t.Context(), "echo installing\necho 'install-sysbox: kernel too old' >&2\nexit 3\n", &out)
	if !errors.As(err, &unsupported) || unsupported.Reason != "kernel too old" || !strings.Contains(out.String(), "installing") {
		t.Fatalf("exit 3 = %v with output %q, want an UnsupportedError and the streamed output", err, out.String())
	}
	got, _ := os.ReadFile(args)
	if !strings.Contains(string(got), "--unit=aether-sysbox-install --collect --wait --quiet") {
		t.Errorf("systemd-run args = %q", got)
	}
	if err := runScript(t.Context(), "exit 1\n", &out); err == nil || errors.As(err, &unsupported) {
		t.Fatalf("exit 1 = %v, want a plain failure", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	var detached *DetachedError
	if err := runScript(ctx, "sleep 5\n", &out); !errors.As(err, &detached) || detached.Unit != unitName {
		t.Fatalf("cancelled wait = %v, want a DetachedError naming the unit", err)
	}
}

func TestRunScriptExitCodes(t *testing.T) {
	var unsupported *UnsupportedError
	var out bytes.Buffer
	err := runScript(t.Context(), "echo installing\necho 'install-sysbox: docker not found; need Docker' >&2\nexit 3\n", &out)
	if !errors.As(err, &unsupported) || unsupported.Reason != "docker not found; need Docker" {
		t.Fatalf("exit 3 = %v, want an UnsupportedError with the script's last message", err)
	}
	if !strings.Contains(out.String(), "installing") {
		t.Errorf("output = %q, want the script's output streamed", out.String())
	}

	err = runScript(t.Context(), "echo 'install-sysbox: checksum mismatch' >&2\nexit 1\n", &out)
	if err == nil || errors.As(err, &unsupported) || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("exit 1 = %v, want a plain failure carrying the last message", err)
	}

	var broken *BrokenError
	err = runScript(t.Context(), "echo 'install-sysbox: purge failed' >&2\nexit 4\n", &out)
	if !errors.As(err, &broken) || broken.Reason != "purge failed" {
		t.Fatalf("exit 4 = %v, want a BrokenError carrying the last message", err)
	}

	if err := runScript(t.Context(), "exit 0\n", &out); err != nil {
		t.Fatalf("exit 0 = %v", err)
	}
}

func TestManagerOutcomeOutranksDockerError(t *testing.T) {
	h := &fakeHost{root: true, err: &UnsupportedError{Reason: "docker not found"}}
	m := newFakeManager(h)
	t.Cleanup(func() { _ = m.Close() })
	close(h.release)
	m.status = func(context.Context) (string, error) { return "", errors.New("Cannot connect to the Docker daemon") }

	if st := m.Status(t.Context()); st.State != protocol.SysboxOff || !strings.Contains(st.Reason, "Cannot connect") {
		t.Fatalf("before any install = %+v, want off with Docker's error", st)
	}
	m.Install(t.Context())
	waitState(t, m, protocol.SysboxUnsupported)
	if st := m.Status(t.Context()); st.State != protocol.SysboxUnsupported || st.Reason != "docker not found" {
		t.Fatalf("after the install = %+v, want the recorded outcome", st)
	}
	m.status = func(context.Context) (string, error) { return protocol.SysboxOn, nil }
	if st := m.Status(t.Context()); st.State != protocol.SysboxOn {
		t.Fatalf("with Sysbox listed = %+v, want on", st)
	}
}

// A Sysbox that Docker lists but that failed its probe is failed, not on, and
// the runtime must not use it, until an install succeeds.
func TestManagerBrokenOutranksOn(t *testing.T) {
	h := &fakeHost{root: true, err: &BrokenError{Reason: "bind mounts are not ID-mapped; purge failed"}}
	m := newFakeManager(h)
	t.Cleanup(func() { _ = m.Close() })
	close(h.release)
	m.status = func(context.Context) (string, error) {
		if h.runs.Load() > 0 {
			return protocol.SysboxOn, nil // the package registered it
		}
		return protocol.SysboxOff, nil
	}

	m.Install(t.Context())
	if st := waitState(t, m, protocol.SysboxFailed); !strings.Contains(st.Reason, "purge failed") || !m.Unusable() {
		t.Fatalf("status = %+v, unusable = %v, want failed and unusable while Docker lists the runtime", st, m.Unusable())
	}
	h.err = nil
	m.Install(t.Context())
	waitState(t, m, protocol.SysboxOn)
	if m.Unusable() {
		t.Error("a successful install left the runtime unusable")
	}
}

// A run started while an install is still checking Sysbox must not use it,
// and every outcome makes the runtime ask Docker again, so a rolled-back
// Sysbox is not requested from a cache.
func TestManagerHoldsRuntimeDuringInstall(t *testing.T) {
	h := &fakeHost{root: true, err: errors.New("probe failed; Sysbox was removed again")}
	m := newFakeManager(h)
	t.Cleanup(func() { _ = m.Close() })
	m.Install(t.Context())
	if !m.Unusable() {
		t.Fatal("Sysbox is usable while its install is still checking it")
	}
	close(h.release)
	waitState(t, m, protocol.SysboxFailed)
	if m.Unusable() {
		t.Error("a rolled-back install left the runtime unusable")
	}
	if h.rechecks.Load() != 1 {
		t.Errorf("rechecks = %d after a failed install, want 1", h.rechecks.Load())
	}
}

type fakeHost struct {
	root     bool
	state    atomic.Value // string
	release  chan struct{}
	err      error
	runs     atomic.Int32
	rechecks atomic.Int32
}

func newFakeManager(h *fakeHost) *Manager {
	h.state.Store(protocol.SysboxOff)
	h.release = make(chan struct{})
	m := NewManager()
	m.root = func() bool { return h.root }
	m.status = func(context.Context) (string, error) { return h.state.Load().(string), nil }
	m.install = func(ctx context.Context, out *tail) error {
		_, _ = out.Write([]byte("working\n"))
		h.runs.Add(1)
		select {
		case <-h.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if h.err == nil {
			h.state.Store(protocol.SysboxOn)
		}
		return h.err
	}
	m.Recheck = func() { h.rechecks.Add(1) }
	return m
}

func waitState(t *testing.T, m *Manager, want string) protocol.SysboxStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := m.Status(t.Context())
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %+v, want %s", st, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerInstallsOnceAndRechecks(t *testing.T) {
	h := &fakeHost{root: true}
	m := newFakeManager(h)
	t.Cleanup(func() { _ = m.Close() })

	if st := m.Install(t.Context()); st.State != protocol.SysboxInstalling {
		t.Fatalf("Install() = %+v, want installing", st)
	}
	for h.runs.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if st := m.Install(t.Context()); st.State != protocol.SysboxInstalling || st.Log != "working\n" {
		t.Fatalf("second Install() = %+v, want the running install and its log", st)
	}
	close(h.release)
	waitState(t, m, protocol.SysboxOn)
	if h.runs.Load() != 1 || h.rechecks.Load() != 1 {
		t.Errorf("installs = %d, rechecks = %d, want 1 and 1", h.runs.Load(), h.rechecks.Load())
	}
	if st := m.Install(t.Context()); st.State != protocol.SysboxOn || h.runs.Load() != 1 {
		t.Errorf("Install() on a host with Sysbox = %+v after %d installs, want on and no new install", st, h.runs.Load())
	}
}

func TestManagerReportsUnsupportedAndFailed(t *testing.T) {
	h := &fakeHost{root: true, err: &UnsupportedError{Reason: "kernel too old"}}
	m := newFakeManager(h)
	t.Cleanup(func() { _ = m.Close() })
	close(h.release)

	m.Install(t.Context())
	if st := waitState(t, m, protocol.SysboxUnsupported); st.Reason != "kernel too old" || st.Log != "working\n" {
		t.Fatalf("status = %+v, want the reason and log", st)
	}

	h.err = errors.New("apt-get update failed")
	m.Install(t.Context())
	if st := waitState(t, m, protocol.SysboxFailed); st.Reason != "apt-get update failed" {
		t.Fatalf("status = %+v, want the failure reason", st)
	}
	if h.runs.Load() != 2 {
		t.Errorf("installs = %d, want a retry after a failure", h.runs.Load())
	}
}

func TestManagerWithoutRootNamesTheCommand(t *testing.T) {
	h := &fakeHost{}
	m := newFakeManager(h)
	for name, st := range map[string]protocol.SysboxStatus{"status": m.Status(t.Context()), "install": m.Install(t.Context())} {
		if st.State != protocol.SysboxOff || !strings.Contains(st.Reason, "sudo aether-server sysbox install") {
			t.Errorf("%s = %+v, want off naming the manual command", name, st)
		}
	}
	if h.runs.Load() != 0 {
		t.Error("a non-root server ran the installer")
	}
}

func TestTailKeepsTheEnd(t *testing.T) {
	var tl tail
	_, _ = tl.Write([]byte(strings.Repeat("a", logLimit)))
	_, _ = tl.Write([]byte("tail"))
	got := tl.String()
	if len(got) != logLimit || !strings.HasSuffix(got, "aatail") {
		t.Errorf("tail holds %d bytes ending %q, want %d ending in the newest", len(got), got[len(got)-6:], logLimit)
	}
}
