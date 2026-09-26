package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	containerruntime "github.com/3xDevOps/Aether/internal/runtime"
)

type recoveryRuntime struct {
	containerruntime.Runtime
	info               containerruntime.BrowserInfo
	created, destroyed bool
}

func (r *recoveryRuntime) CreateBrowser(context.Context, containerruntime.BrowserSpec) (containerruntime.ID, error) {
	r.created = true
	return "", errors.New("unexpected companion creation")
}
func (r *recoveryRuntime) InspectBrowser(context.Context, containerruntime.ID) (containerruntime.BrowserInfo, error) {
	return r.info, nil
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
	if err := manager.Remove(t.Context(), run); err == nil {
		t.Fatal("removed a companion owned by another run")
	}
	if runtime.destroyed {
		t.Fatal("ownership failure reached destruction")
	}
}
