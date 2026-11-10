package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type companionRuntime struct {
	*fakeRuntime
	info runtime.BrowserInfo
}

func (r *companionRuntime) CreateBrowser(context.Context, runtime.BrowserSpec) (runtime.ID, error) {
	return "", errors.New("unexpected companion creation")
}

func (r *companionRuntime) InspectBrowser(context.Context, runtime.ID) (runtime.BrowserInfo, error) {
	return r.info, nil
}

func TestBrowserInspectReadsWithoutControlAndNamesAnOlderCompanion(t *testing.T) {
	// The companion socket's path must stay under the 108-byte AF_UNIX bound.
	state, err := os.MkdirTemp("", "inspect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	var rt *companionRuntime
	e := newTestEnv(t, func(c *Config) {
		rt = &companionRuntime{fakeRuntime: c.Runtime.(*fakeRuntime)}
		c.Runtime = rt
		c.BrowserImage = "aether/browser:test"
		c.Control = control.New(control.Config{})
		c.StateDir = state
	})
	run, _ := e.launchFake(t, "inspect")
	live, err := e.sched.ResolveLiveRun(t.Context(), run.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	rt.info = runtime.BrowserInfo{ContainerID: "companion", RunContainer: live.ContainerID, CreationKey: "creation", State: "running"}
	digest := sha256.Sum256([]byte(run.ID))
	dir := filepath.Join(state, "browser", hex.EncodeToString(digest[:16]))
	if err = os.MkdirAll(filepath.Join(dir, "control"), 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := json.Marshal(browser.Status{RunID: string(run.ID), RunContainer: live.ContainerID, ContainerID: "companion", CreationKey: "creation", SessionID: "session", ProcessID: "process", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "state.json"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
	var older atomic.Bool
	listener, err := net.Listen("unix", filepath.Join(dir, "control", "browser.sock"))
	if err != nil {
		t.Fatal(err)
	}
	companion := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(browser.Health{CreationKey: "creation", ProcessID: "process", SessionID: "session", ProtocolVersion: 1})
			return
		}
		var request browser.Request
		if decodeErr := json.NewDecoder(r.Body).Decode(&request); decodeErr != nil || request.Operation != "inspect" || request.ViewportID != "viewport" || request.X != 45 || request.Y != 34 {
			t.Errorf("companion request = %+v, %v", request, decodeErr)
		}
		if older.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"Unknown browser operation"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"page":{"session_id":"session","page_id":"page","page_revision":1,"viewport_id":"viewport","url":"http://127.0.0.1:3000/","title":"","width":800,"height":600},"element":{"tag":"button","role":"button","name":"Save changes","text":"Save","selector":"button#save","attributes":{"id":"save"},"bounds":{"x":40,"y":30,"width":120,"height":36},"html":"<button id=\"save\">Save</button>","styles":{"display":"block"},"component":"App > SaveButton","source":"src/SaveButton.tsx:12:5","frame_url":""}}`))
	})}
	go func() { _ = companion.Serve(listener) }()
	t.Cleanup(func() { _ = companion.Close() })

	agent := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	if _, _, err = e.sched.cfg.Control.AcquireSurface(string(run.ID), browserSurface("session"), agent, "agent-controller", false, 0, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(protocol.DevBrowserInspectParams{
		DevBrowserPageTarget: protocol.DevBrowserPageTarget{DevBrowserTarget: protocol.DevBrowserTarget{SessionID: "session"}, PageID: "page", PageRevision: 1},
		ViewportID:           "viewport", X: 45, Y: 34,
	})
	if err != nil {
		t.Fatal(err)
	}
	watcher := control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}
	result, err := e.sched.CallDevelopment(t.Context(), run.ID, watcher, protocol.MethodDevBrowserInspect, raw, func() error { return nil })
	if err != nil {
		t.Fatalf("inspect while the agent holds the browser: %v", err)
	}
	element := result.(protocol.DevBrowserInspectResult).Element
	if element == nil || element.Selector != "button#save" || element.Source != "src/SaveButton.tsx:12:5" || element.Bounds != (protocol.DevBrowserRect{X: 40, Y: 30, Width: 120, Height: 36}) || element.Attributes["id"] != "save" {
		t.Fatalf("element = %+v", element)
	}

	older.Store(true)
	_, err = e.sched.CallDevelopment(t.Context(), run.ID, watcher, protocol.MethodDevBrowserInspect, raw, func() error { return nil })
	if !errors.Is(err, ErrBrowserInspectUnsupported) || DevelopmentErrorCode(err) != protocol.CodeMethodNotFound || !strings.Contains(err.Error(), "Unknown browser operation") {
		t.Fatalf("older companion: %v", err)
	}
}
