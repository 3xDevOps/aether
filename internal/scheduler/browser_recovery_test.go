package scheduler

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func serveCompanion(t *testing.T, dir string, post http.HandlerFunc) {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(dir, "control", "browser.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(browser.Health{CreationKey: "browser-companion", ProcessID: "process", SessionID: "session", ProtocolVersion: 1})
			return
		}
		post(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

func TestStartupRecoversEverySessionWhenACompanionCannotReleaseInput(t *testing.T) {
	// The companion socket's path must stay under the 108-byte AF_UNIX bound.
	state, err := os.MkdirTemp("", "recover-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	var rt *sweptBrowserRuntime
	e := newTestEnv(t, func(c *Config) {
		rt = &sweptBrowserRuntime{fakeRuntime: c.Runtime.(*fakeRuntime), browsers: make(map[runtime.ID]runtime.BrowserInfo)}
		c.Runtime = rt
		c.BrowserImage = "aether/browser:test"
		c.Control = control.New(control.Config{})
		c.StateDir = state
	})
	saturated, saturatedContainer := e.launchFake(t, "saturated companion")
	hung, hungContainer := e.launchFake(t, "hung companion")
	healthy, healthyContainer := e.launchFake(t, "healthy companion")
	var released atomic.Bool
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	for run, serve := range map[*domain.Run]struct {
		container runtime.ID
		post      http.HandlerFunc
	}{
		saturated: {saturatedContainer.id, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"resource_limit","message":"Companion operation queue is full"}}`))
		}},
		hung: {hungContainer.id, func(http.ResponseWriter, *http.Request) { <-blocked }},
		healthy: {healthyContainer.id, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/release-input" {
				t.Errorf("healthy companion got %s", r.URL.Path)
			}
			released.Store(true)
			_, _ = w.Write([]byte(`{"session_id":"session","released":true}`))
		}},
	} {
		companion := runtime.ID("companion-" + string(run.ID))
		journal, jerr := json.Marshal(browser.Status{RunID: string(run.ID), RunContainer: serve.container, ContainerID: companion, CreationKey: "browser-companion", SessionID: "session", ProcessID: "process", State: "running"})
		if jerr != nil {
			t.Fatal(jerr)
		}
		dir := browserJournalDir(e, run.ID)
		if err = os.MkdirAll(filepath.Join(dir, "control"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "state.json"), journal, 0o600); err != nil {
			t.Fatal(err)
		}
		rt.mu.Lock()
		rt.browsers[companion] = runtime.BrowserInfo{ContainerID: companion, RunContainer: serve.container, CreationKey: "browser-companion", State: "running"}
		rt.mu.Unlock()
		serveCompanion(t, dir, serve.post)
	}

	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	startScheduler(t, next)
	select {
	case <-next.RecoveryReady():
	case <-time.After(45 * time.Second):
		t.Fatal("startup recovery did not finish")
	}

	if !released.Load() {
		t.Error("the healthy companion was never asked to release input")
	}
	watcher := control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}
	for _, run := range []*domain.Run{saturated, hung} {
		raw := json.RawMessage(`{"run_id":"` + string(run.ID) + `"}`)
		result, callErr := next.CallDevelopment(t.Context(), run.ID, watcher, protocol.MethodDevBrowserStatus, raw, func() error { return nil })
		if callErr != nil {
			t.Fatalf("browser status of %s: %v", run.ID, callErr)
		}
		status := result.(protocol.DevBrowserStatusResult)
		if status.Available || status.State != "unavailable" || !strings.Contains(status.Reason, "session "+string(run.ID)+" browser companion companion-"+string(run.ID)) {
			t.Errorf("browser status of %s = %+v", run.ID, status)
		}
		if resumeErr := next.resumeDevelopmentBrowser(t.Context(), run.ID, next.developmentContainer(run.ID)); resumeErr != nil {
			t.Errorf("resume %s with its browser unavailable: %v", run.ID, resumeErr)
		}
		// A reset needs the surface; a companion that cannot release input
		// must not keep it fenced.
		if _, _, acquireErr := e.cfg.Control.AcquireSurface(string(run.ID), browserSurface(status.SessionID), watcher, "control-session", false, 0, func() error { return nil }); acquireErr != nil {
			t.Errorf("acquire %s's browser to reset it: %v", run.ID, acquireErr)
		}
	}
	if _, _, err = next.browserClient(t.Context(), LiveRun{Run: *healthy, ContainerID: healthyContainer.id}, false); err != nil {
		t.Errorf("healthy session's browser after recovery: %v", err)
	}
}
