package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	containerruntime "github.com/3xDevOps/Aether/internal/runtime"
)

type recoveryRuntime struct {
	containerruntime.Runtime
	info               containerruntime.BrowserInfo
	created, destroyed bool
	inspectErr         error
}

func (r *recoveryRuntime) CreateBrowser(context.Context, containerruntime.BrowserSpec) (containerruntime.ID, error) {
	r.created = true
	return "", errors.New("unexpected companion creation")
}
func (r *recoveryRuntime) InspectBrowser(context.Context, containerruntime.ID) (containerruntime.BrowserInfo, error) {
	return r.info, r.inspectErr
}
func (r *recoveryRuntime) FindByCreationKey(context.Context, string) (containerruntime.ID, error) {
	return "companion", nil
}
func (r *recoveryRuntime) Destroy(context.Context, containerruntime.ID) error {
	r.destroyed = true
	return nil
}

func serveUnix(t *testing.T, path string, handler http.Handler) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

func TestManagerRecoveryDistinguishesResetFromProcessLoss(t *testing.T) {
	for _, test := range []struct {
		name, process string
		lost          bool
	}{{"explicit-context-reset", "process-one", false}, {"companion-restart", "process-two", true}} {
		t.Run(test.name, func(t *testing.T) {
			// Keep the AF_UNIX path under Linux's 108-byte bound even when the
			// Go test's generated temporary directory embeds a long test name.
			root, err := os.MkdirTemp("", "browser-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			runtime := &recoveryRuntime{info: containerruntime.BrowserInfo{ContainerID: "companion", RunContainer: "run-container", CreationKey: "creation", State: "running"}}
			manager, err := NewManager(root, runtime, Config{Image: "aether/browser:test"})
			if err != nil {
				t.Fatal(err)
			}
			run := Run{ID: "run", ContainerID: "run-container"}
			_, dir, err := manager.entry(run)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Join(dir, "control"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err = saveStatus(dir, Status{RunID: run.ID, RunContainer: run.ContainerID, CreationKey: "creation", SessionID: "old-session", ProcessID: "process-one", State: "running"}); err != nil {
				t.Fatal(err)
			}
			serveUnix(t, filepath.Join(dir, "control", "browser.sock"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(Health{CreationKey: "creation", ProcessID: test.process, SessionID: "new-session", ProtocolVersion: 1})
			}))
			status, client, err := manager.Ensure(t.Context(), run)
			if test.lost {
				if !errors.Is(err, ErrUnavailable) || status.State != "session_lost" || client != nil {
					t.Fatalf("restart = %+v, %v, %v", status, client, err)
				}
				stored, readErr := loadStatus(dir)
				if readErr != nil || stored.State != "session_lost" || stored.ProcessID != "process-one" {
					t.Fatalf("session loss not durable: %+v, %v", stored, readErr)
				}
			} else {
				if err != nil || status.SessionID != "new-session" || status.ContainerID != "companion" || client == nil {
					t.Fatalf("reset reconciliation = %+v, %v", status, err)
				}
				stored, readErr := loadStatus(dir)
				if readErr != nil || stored.SessionID != "new-session" || stored.ContainerID != "companion" || stored.ProcessID != "process-one" {
					t.Fatalf("reset reconciliation not durable: %+v, %v", stored, readErr)
				}
				client.Close()
			}
			if runtime.created || runtime.destroyed {
				t.Fatal("recovery silently replaced the browser")
			}
		})
	}
}

func TestManagerRefusesRemovingAnotherRunCompanion(t *testing.T) {
	runtime := &recoveryRuntime{info: containerruntime.BrowserInfo{RunContainer: "other-run", CreationKey: "creation", State: "running"}}
	manager, err := NewManager(t.TempDir(), runtime, Config{Image: "aether/browser:test"})
	if err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "run", ContainerID: "run-container"}
	_, dir, err := manager.entry(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveStatus(dir, Status{RunID: run.ID, RunContainer: run.ContainerID, ContainerID: "companion", CreationKey: "creation"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(t.Context(), run.ID); err == nil {
		t.Fatal("removed a companion owned by another run")
	}
	if runtime.destroyed {
		t.Fatal("ownership failure reached destruction")
	}
}

func TestManagerRemoveObservedKeepsAReplacementCompanion(t *testing.T) {
	runtime := &recoveryRuntime{info: containerruntime.BrowserInfo{RunContainer: "new-container", CreationKey: "new", State: "running"}}
	manager, err := NewManager(t.TempDir(), runtime, Config{Image: "aether/browser:test"})
	if err != nil {
		t.Fatal(err)
	}
	dir := manager.journalDir("run")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = saveStatus(dir, Status{RunID: "run", RunContainer: "new-container", ContainerID: "companion", CreationKey: "new"}); err != nil {
		t.Fatal(err)
	}
	observed := Status{RunID: "run", RunContainer: "old-container", ContainerID: "old-companion", CreationKey: "old"}
	if err = manager.RemoveObserved(t.Context(), observed); !errors.Is(err, ErrReplaced) {
		t.Fatalf("remove of a replaced observation: %v, want ErrReplaced", err)
	}
	if runtime.destroyed {
		t.Fatal("removing a stale observation destroyed the replacement companion")
	}
	current, err := loadStatus(dir)
	if err != nil {
		t.Fatalf("replacement journal: %v", err)
	}
	if err = manager.RemoveObserved(t.Context(), current); err != nil || !runtime.destroyed {
		t.Fatalf("remove of the current observation: %v, destroyed %v", err, runtime.destroyed)
	}
}

func TestInputSessionGoneRequiresPhysicalProof(t *testing.T) {
	status := Status{ContainerID: "companion", CreationKey: "creation", RunContainer: "run"}
	for _, tc := range []struct {
		name     string
		state    string
		creation string
		err      error
		gone     bool
	}{
		{"running", "running", "creation", nil, false},
		{"paused", "paused", "creation", nil, false},
		{"unreachable", "exited", "creation", errors.New("runtime unavailable"), false},
		{"foreign", "exited", "other", nil, false},
		{"exited", "exited", "creation", nil, true},
		{"removed", "", "", containerruntime.ErrNotFound, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &recoveryRuntime{info: containerruntime.BrowserInfo{ContainerID: "companion", RunContainer: "run", CreationKey: tc.creation, State: tc.state}, inspectErr: tc.err}
			manager, err := NewManager(t.TempDir(), runtime, Config{Image: "aether/browser:test"})
			if err != nil {
				t.Fatal(err)
			}
			if got := manager.InputSessionGone(t.Context(), status); got != tc.gone {
				t.Fatalf("physical input state gone = %v, want %v", got, tc.gone)
			}
		})
	}
}

func TestMarkUnavailableKeepsThePausedCompanionsErrorReadable(t *testing.T) {
	runtime := &recoveryRuntime{info: containerruntime.BrowserInfo{ContainerID: "companion", RunContainer: "run-container", CreationKey: "creation", State: "paused"}}
	manager, err := NewManager(t.TempDir(), runtime, Config{Image: "aether/browser:test"})
	if err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "run", ContainerID: "run-container"}
	_, dir, err := manager.entry(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveStatus(dir, Status{RunID: run.ID, RunContainer: run.ContainerID, ContainerID: "companion", CreationKey: "creation", State: "running"}); err != nil {
		t.Fatal(err)
	}
	reason := "startup recovery failed: " + strings.Repeat("x", 16384)
	if err := manager.MarkUnavailable(run, reason); err != nil {
		t.Fatal(err)
	}
	status, _, err := manager.Reconcile(t.Context(), run)
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "startup recovery failed: xxx") {
		t.Fatalf("Reconcile = %v, want the recorded startup error", err)
	}
	if status.State != "paused" || status.Reason != status.Unavailable {
		t.Fatalf("status = %+v, want paused with the recorded error", status)
	}
	if err := manager.Remove(t.Context(), run.ID); err != nil || !runtime.destroyed {
		t.Fatalf("Remove = %v, destroyed %v: the marked journal must stay readable", err, runtime.destroyed)
	}
}
