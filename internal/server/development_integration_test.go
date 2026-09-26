//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// This is an executable broker smoke, not a companion-only or source-shape
// test: real SSH admission, owned run PTY, run-loopback browser, capture mount,
// agent-vs-human control and pause/resume all share one headless environment.
func TestIntegrationHeadlessDevelopmentBroker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("headless broker smoke requires Docker")
	}
	image, _ := buildCoordAgentImage(t)
	docker, _, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("Docker disappeared")
	}
	browserImage := os.Getenv("AETHER_BROWSER_TEST_IMAGE")
	if browserImage == "" {
		browserImage = DefaultBrowserImage
	}
	e := &coordEnv{rt: docker, image: image, browserImage: browserImage, serverBinary: buildServerBinary(t), dataDir: filepath.Join(shortTempDir(t), "data")}
	server := e.seed(ctx, t, true)
	ctrl, _ := server.control(t, e.ada.key)
	run := e.launch(t, ctrl, "headless development broker", "claude")
	id := domain.RunID(run.ID)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if err := server.srv.sched.CloseRun(cleanupCtx, id, e.ada.id, domain.RunAbandoned); err != nil {
			t.Errorf("clean development run: %v", err)
		}
	})
	params := protocol.DevRunParams{RunID: run.ID}
	execution, resolveErr := server.srv.sched.ResolveLiveRun(ctx, id, false)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	statusCode, statusJSON, statusErrText, statusErr := docker.Exec(ctx, execution.ContainerID, []string{"/usr/local/bin/aether-internal", "status"}, execution.Workdir)
	if statusErr != nil || statusCode != 0 {
		t.Fatalf("live agent discovery: %d %v %s", statusCode, statusErr, statusErrText)
	}
	var discovery struct {
		OK     bool                       `json:"ok"`
		Result protocol.CoordStatusResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(statusJSON), &discovery); err != nil {
		t.Fatal(err)
	}
	advertised := false
	for _, capability := range discovery.Result.Capabilities {
		if capability == protocol.MethodDevBrowserOpen {
			advertised = true
		}
	}
	if !discovery.OK || discovery.Result.RunID != run.ID || !advertised {
		t.Fatalf("development unavailable with conflict policy disabled: %s", statusJSON)
	}
	var terminal protocol.DevTerminalStartResult
	// Keep shell quoting literal and do not depend on a project runtime.
	app := `mkdir -p /tmp/aether-broker-app && printf '%s' '<!doctype html><title>Broker app</title><button onclick="this.textContent=String.fromCharCode(67,111,110,102,105,114,109,101,100)">Verify</button>' > /tmp/aether-broker-app/index.html && printf 'HTTPD_READY\n' && exec httpd -f -p 127.0.0.1:31872 -h /tmp/aether-broker-app`
	if err := ctrl.Call(protocol.MethodDevTerminalStart, protocol.DevTerminalStartParams{DevRunParams: params, Name: "web-smoke", Command: []string{"/bin/sh", "-c", app}, Cols: 90, Rows: 28}, &terminal); err != nil {
		t.Fatal(err)
	}
	target := protocol.DevTerminalTarget{DevRunParams: params, TerminalID: terminal.Terminal.TerminalID, Incarnation: terminal.Terminal.Incarnation}
	var waited protocol.DevTerminalWaitResult
	if err := ctrl.Call(protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{DevTerminalTarget: target, Contains: "HTTPD_READY", TimeoutMS: 10000}, &waited); err != nil {
		t.Fatal(err)
	}
	if !waited.Matched {
		t.Fatalf("app readiness was not observed: %+v", waited)
	}
	var opened protocol.DevBrowserOpenResult
	if err := ctrl.Call(protocol.MethodDevBrowserOpen, protocol.DevBrowserOpenParams{DevRunParams: params, DevControlFence: protocol.DevControlFence{ControlSessionID: "headless-smoke-human"}, URL: "http://127.0.0.1:31872", Width: 800, Height: 600}, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.Control.ControlGeneration == 0 || opened.Page.Title != "Broker app" {
		t.Fatalf("browser bootstrap result: %+v", opened)
	}
	page := protocol.DevBrowserPageTarget{DevBrowserTarget: protocol.DevBrowserTarget{DevRunParams: params, SessionID: opened.Page.SessionID}, PageID: opened.Page.PageID, PageRevision: opened.Page.PageRevision}
	var snapshot protocol.DevBrowserSnapshotResult
	if err := ctrl.Call(protocol.MethodDevBrowserSnapshot, protocol.DevBrowserSnapshotParams{DevBrowserPageTarget: page}, &snapshot); err != nil {
		t.Fatal(err)
	}
	node := ""
	for _, candidate := range snapshot.Nodes {
		if candidate.Role == "button" && candidate.Name == "Verify" {
			node = candidate.NodeID
			break
		}
	}
	if node == "" {
		t.Fatalf("real DOM has no Verify button: %+v", snapshot)
	}
	page.PageRevision = snapshot.Page.PageRevision
	var clicked protocol.DevBrowserActionResult
	if err := ctrl.Call(protocol.MethodDevBrowserAction, protocol.DevBrowserActionParams{DevBrowserPageTarget: page, DevControlFence: opened.Control, Action: "click", NodeID: node}, &clicked); err != nil {
		t.Fatal(err)
	}
	page.PageRevision = clicked.Page.PageRevision
	var observed protocol.DevBrowserWaitResult
	if err := ctrl.Call(protocol.MethodDevBrowserWait, protocol.DevBrowserWaitParams{DevBrowserPageTarget: page, Condition: "text", Text: "Confirmed", TimeoutMS: 10000}, &observed); err != nil {
		t.Fatal(err)
	}
	if !observed.Matched {
		t.Fatalf("real click did not change DOM: %+v", observed)
	}
	page.PageRevision = observed.Page.PageRevision
	var screenshot protocol.DevBrowserScreenshotResult
	if err := ctrl.Call(protocol.MethodDevBrowserScreenshot, protocol.DevBrowserScreenshotParams{DevBrowserPageTarget: page}, &screenshot); err != nil {
		t.Fatal(err)
	}
	human := control.Principal{Kind: control.PrincipalMember, MemberID: e.ada.id}
	_, reader, err := server.srv.sched.OpenDevelopmentArtifact(ctx, id, human, screenshot.Artifact.ID, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	captured, decodeErr := png.Decode(reader)
	_ = reader.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if captured.Bounds().Dx() != 800 || captured.Bounds().Dy() != 600 {
		t.Fatalf("capture geometry = %v", captured.Bounds())
	}
	live, err := server.srv.sched.ResolveLiveRun(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr, err := docker.Exec(ctx, live.ContainerID, []string{"/bin/sh", "-c", `test -r "$1" && ! printf x >> "$1"`, "capture-check", screenshot.Artifact.Path}, live.Workdir)
	if err != nil || code != 0 {
		t.Fatalf("run capture mount is not readable and read-only: code=%d err=%v stderr=%s", code, err, stderr)
	}
	takeover, _ := json.Marshal(protocol.DevControlAcquireParams{DevControlStatusParams: protocol.DevControlStatusParams{Surface: protocol.DevSurface{Kind: "browser", ID: "browser", Incarnation: opened.Page.SessionID}}, ControlSessionID: "run-agent", Takeover: true})
	if _, err = server.srv.sched.HandleAgent(ctx, id, protocol.MethodDevControlAcquire, takeover); !errors.Is(err, control.ErrAgentTakeover) {
		t.Fatalf("agent displaced human control: %v", err)
	}
	var terminalImage protocol.DevTerminalScreenshotResult
	if err := ctrl.Call(protocol.MethodDevTerminalScreenshot, protocol.DevTerminalScreenshotParams{DevTerminalTarget: target}, &terminalImage); err != nil {
		t.Fatal(err)
	}
	if terminalImage.Artifact.Incarnation != target.Incarnation || terminalImage.Artifact.Cols != 90 || terminalImage.Artifact.Rows != 28 {
		t.Fatalf("terminal capture boundary: %+v", terminalImage.Artifact)
	}
	if err = server.srv.sched.Pause(ctx, id, e.ada.id); err != nil {
		t.Fatal(err)
	}
	var paused protocol.DevBrowserStatusResult
	if err := ctrl.Call(protocol.MethodDevBrowserStatus, protocol.DevBrowserStatusParams{DevRunParams: params}, &paused); err != nil {
		t.Fatal(err)
	}
	if paused.Running || paused.State != "paused" {
		t.Fatalf("browser traffic not paused: %+v", paused)
	}
	if err = server.srv.sched.Resume(ctx, id, e.ada.id); err != nil {
		t.Fatal(err)
	}
	var resumed protocol.DevBrowserStatusResult
	if err := ctrl.Call(protocol.MethodDevBrowserStatus, protocol.DevBrowserStatusParams{DevRunParams: params}, &resumed); err != nil {
		t.Fatal(err)
	}
	if !resumed.Running || resumed.SessionID != opened.Page.SessionID {
		t.Fatalf("resume replaced browser state: %+v", resumed)
	}
	if err = server.srv.sched.CloseRun(ctx, id, e.ada.id, domain.RunAbandoned); err != nil {
		t.Fatal(err)
	}
}
