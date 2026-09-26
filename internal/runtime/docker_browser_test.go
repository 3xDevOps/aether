//go:build integration

package runtime_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/moby/moby/client"
)

// Build first: docker build -t aether/browser:test -f images/browser/Dockerfile .
// Run on a headless Ubuntu Docker host as the server's root user:
// go test -tags=integration ./internal/runtime -run TestDockerBrowserCompanion -v
// No DISPLAY, X11, Wayland, Xvfb or host browser installation is used.
func TestDockerBrowserCompanion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	image := os.Getenv("AETHER_BROWSER_TEST_IMAGE")
	if image == "" {
		image = "aether/browser:test"
	}
	docker, err := runtime.NewDocker(runtime.WithNetworkMode("none"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	app, err := docker.Create(ctx, runtime.Spec{Image: image, User: "1000:1000", CPULimit: 0.5, MemoryLimitBytes: 128 << 20, Command: []string{"node", "-e", `require('http').createServer((q,s)=>{s.setHeader('Content-Type','text/html');s.end('<title>Run loopback</title><button onclick="this.textContent=\'Clicked\'">Click me</button>')}).listen(31871,'127.0.0.1')`}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Destroy(context.Background(), app) })
	if err := docker.Start(ctx, app); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("", "browser-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	config := browser.Config{Image: image, CPULimit: 1, MemoryLimitBytes: 2 << 30}
	manager, err := browser.NewManager(root, docker, config)
	if err != nil {
		t.Fatal(err)
	}
	run := browser.Run{ID: "integration-run", ContainerID: app}
	t.Cleanup(func() { _ = manager.Remove(context.Background(), run) })
	status, control, err := manager.Ensure(ctx, run)
	if err != nil {
		t.Fatalf("sandboxed companion startup (private UID 1000 bind requires server/root authority): %v", err)
	}
	defer control.Close()
	info, err := docker.InspectBrowser(ctx, status.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if info.RunContainer != app || info.CreationKey != status.CreationKey || info.State != "running" || !strings.HasPrefix(info.Image, "sha256:") {
		t.Fatalf("companion inspect: %+v", info)
	}
	found, err := docker.FindByCreationKey(ctx, status.CreationKey)
	if err != nil || found != status.ContainerID {
		t.Fatalf("creation recovery: %q, %v", found, err)
	}
	sdk, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer sdk.Close()
	inspected, err := sdk.ContainerInspect(ctx, string(status.ContainerID), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	host := inspected.Container.HostConfig
	if inspected.Container.Config.User != "1000:1000" || !host.ReadonlyRootfs || host.Privileged || host.IpcMode != "private" || host.ShmSize != runtime.BrowserSharedMemoryBytes || string(host.NetworkMode) != "container:"+string(app) || !slices.Contains(host.CapDrop, "ALL") || len(host.CapAdd) != 0 || host.NanoCPUs <= 0 || host.Memory <= 0 || len(host.PortBindings) != 0 {
		t.Fatalf("unsafe browser isolation: %+v", host)
	}
	for _, mount := range inspected.Container.Mounts {
		if string(mount.Type) == "bind" && mount.Destination != "/aether-control" {
			t.Fatalf("unexpected host bind: %+v", mount)
		}
	}
	opened, err := control.Do(ctx, browser.Request{Operation: "open", URL: "http://127.0.0.1:31871"})
	if err != nil {
		t.Fatalf("shared run loopback: %v", err)
	}
	target := browser.Request{SessionID: opened.Page.SessionID, PageID: opened.Page.PageID, PageRevision: opened.Page.PageRevision}
	target.Operation = "snapshot"
	snapshot, err := control.Do(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range snapshot.Snapshot.Nodes {
		if node.Role == "button" && node.Name == "Click me" {
			target.NodeID = node.NodeID
		}
	}
	if target.NodeID == "" {
		t.Fatal("loopback app's real button was not observed")
	}
	target.Operation = "click"
	if _, err := control.Do(ctx, target); err != nil {
		t.Fatal(err)
	}
	target.Operation, target.Condition, target.Text = "wait", "text", "Clicked"
	waited, err := control.Do(ctx, target)
	if err != nil || !waited.Matched {
		t.Fatalf("app interaction: %+v, %v", waited, err)
	}
	if _, err := control.Capture(ctx, target); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr, err := docker.Exec(ctx, app, []string{"node", "-e", `if(require('fs').existsSync('/aether-control'))process.exit(1);require('net').connect(9222,'127.0.0.1').on('connect',()=>process.exit(2)).on('error',()=>process.exit(0))`}, "")
	if err != nil || code != 0 {
		t.Fatalf("run can access companion control/debug TCP: %d %s %s %v", code, stdout, stderr, err)
	}
	if _, err := manager.Pause(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resume(ctx, run); err != nil {
		t.Fatal(err)
	}
	recovered, err := browser.NewManager(root, docker, config)
	if err != nil {
		t.Fatal(err)
	}
	recoveredStatus, recoveredClient, err := recovered.Reconcile(ctx, run)
	if err != nil || recoveredStatus.SessionID != status.SessionID {
		t.Fatalf("server restart lost live context: %+v %v", recoveredStatus, err)
	}
	defer recoveredClient.Close()
	reset, err := recoveredClient.Do(ctx, browser.Request{Operation: "reset", SessionID: status.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	reconciled, _, err := recovered.Reconcile(ctx, run)
	if err != nil || reconciled.SessionID != reset.SessionID {
		t.Fatalf("intentional reset mistaken for process loss: %+v %v", reconciled, err)
	}
	code, stdout, stderr, err = docker.Exec(ctx, status.ContainerID, []string{"node", "/opt/aether-browser/smoke.mjs"}, "")
	if err != nil || code != 0 {
		t.Fatalf("headless sandbox/image/terminal smoke: %d %s %s %v", code, stdout, stderr, err)
	}
	t.Logf("headless companion smoke: %s", stdout)
	if err := docker.Stop(ctx, status.ContainerID, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := docker.Start(ctx, status.ContainerID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if health, err := control.Health(ctx); err == nil && health.ProcessID != status.ProcessID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restarted companion did not become healthy")
		}
		time.Sleep(100 * time.Millisecond)
	}
	lost, _, err := recovered.Ensure(ctx, run)
	if !errors.Is(err, browser.ErrUnavailable) || lost.State != "session_lost" || lost.ContainerID != status.ContainerID {
		t.Fatalf("process restart not reported as explicit session loss: %+v %v", lost, err)
	}
}

func TestDockerBrowserRejectsHostNetwork(t *testing.T) {
	image := os.Getenv("AETHER_BROWSER_TEST_IMAGE")
	if image == "" {
		image = "aether/browser:test"
	}
	docker, err := runtime.NewDocker(runtime.WithNetworkMode("host"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	run, err := docker.Create(t.Context(), runtime.Spec{Image: image, Command: []string{"sleep", "60"}, CPULimit: 0.1, MemoryLimitBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Destroy(context.Background(), run) })
	if err := docker.Start(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	_, err = docker.CreateBrowser(t.Context(), runtime.BrowserSpec{RunContainer: run, Image: image, CreationKey: "reject-host", ControlHostPath: t.TempDir(), CPULimit: 1, MemoryLimitBytes: 1 << 30})
	if err == nil || !strings.Contains(err.Error(), "cannot join host networking") {
		t.Fatalf("host-network browser creation = %v", err)
	}
}
