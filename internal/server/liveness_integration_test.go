//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestIntegrationSilentModelParksAsPossibleStall runs real Claude Code in
// Enhanced mode against a model that takes every request and never answers:
// the prompt is accepted, then nothing. Past -first-event-grace the run
// parks as a possible stall, waiting by stall, with its connection open, and
// never reads disconnected.
func TestIntegrationSilentModelParksAsPossibleStall(t *testing.T) {
	image := os.Getenv("AETHER_AGENTS_IMAGE")
	if image == "" {
		t.Skip("AETHER_AGENTS_IMAGE unset; build the image with make agents-image (docs/testing.md)")
	}
	if !dockerReachable(t) {
		t.Fatal("AETHER_AGENTS_IMAGE is set but the Docker daemon is unreachable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	docker, err := runtime.NewDocker(runtime.WithLabels(map[string]string{"aether.test": t.Name()}))
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	cli := newDockerCLI(t)
	label := "aether.test=" + t.Name()
	removeLabeledContainers(t, cli, label)
	t.Cleanup(func() { removeLabeledContainers(t, cli, label) })

	t.Setenv("AETHER_FAKE_AGENT", "sleep 600")
	const grace = 20 * time.Second
	e := &coordEnv{
		rt:           docker,
		image:        image,
		serverBinary: buildServerBinary(t),
		dataDir:      filepath.Join(shortTempDir(t), "data"),
		tune: func(cfg *Config) {
			cfg.FirstEventGrace, cfg.PollInterval = grace, 2*time.Second
		},
	}
	srv := e.seed(ctx, t, false)
	ctrl, _ := srv.control(t, e.ada.key)
	// The agent network exists once a run has launched; its gateway is the
	// host as run containers reach it.
	e.launch(t, ctrl, "create the agent network", "fake")
	network, err := cli.NetworkInspect(ctx, runtime.AgentNetwork, client.NetworkInspectOptions{})
	if err != nil || len(network.Network.IPAM.Config) == 0 {
		t.Fatalf("inspect %s: %+v, %v", runtime.AgentNetwork, network, err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(network.Network.IPAM.Config[0].Gateway.String(), "0"))
	if err != nil {
		t.Fatalf("listen for the silent model: %v", err)
	}
	var requests atomic.Int64
	release := make(chan struct{})
	model := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})}}
	model.Start()
	t.Cleanup(model.Close)
	t.Cleanup(func() { close(release) })
	t.Setenv("ANTHROPIC_API_KEY", realAgentKey)
	t.Setenv("ANTHROPIC_BASE_URL", model.URL)
	claudeState, err := json.Marshal(map[string]any{
		"hasCompletedOnboarding": true,
		"customApiKeyResponses":  map[string][]string{"approved": {realAgentKey[len(realAgentKey)-20:]}},
		"projects":               map[string]any{domain.PrimaryRepoPath: map[string]bool{"hasTrustDialogAccepted": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".claude.json":          string(claudeState),
		".claude/settings.json": `{"skipDangerousModePermissionPrompt": true}`,
	} {
		path := filepath.Join(e.dataDir, "homes", string(e.ada.id), name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var launched protocol.RunLaunchResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "say hello", Harness: "claude", Mode: string(domain.LaunchACP),
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	get := func() protocol.Run {
		var res protocol.RunResult
		if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: launched.Run.ID}, &res); err != nil {
			t.Fatalf("run.get: %v", err)
		}
		return res.Run
	}
	var run protocol.Run
	for {
		run = get()
		if domain.RunActivity(run.Activity) == domain.ActivityDisconnected {
			t.Fatalf("a silent model disconnected the run: %+v", run)
		}
		if run.Status == string(domain.RunNeedsAttention) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no possible stall: %v; run %+v, model requests %d", ctx.Err(), run, requests.Load())
		case <-time.After(time.Second):
		}
	}
	if requests.Load() == 0 {
		t.Fatal("the run parked before Claude Code asked the model anything")
	}
	l := run.Liveness
	if !strings.HasPrefix(run.Reason, "stalled: possibly — no progress for ") ||
		!strings.HasSuffix(run.Reason, ", awaiting first event, transport open") ||
		domain.RunActivity(run.Activity) != domain.ActivityWaiting || run.Cause != string(events.CauseStall) ||
		l == nil || l.Phase != string(domain.PhaseFirstEvent) || l.Transport != domain.TransportOpen || l.LastProgressAt != nil {
		t.Fatalf("parked as %q, %q by %q, liveness %+v; want a possible stall awaiting the first event", run.Reason, run.Activity, run.Cause, l)
	}
	t.Logf("parked after %d model requests: %q, liveness %+v", requests.Load(), run.Reason, *l)
	time.Sleep(3 * time.Second)
	if run = get(); domain.RunActivity(run.Activity) == domain.ActivityDisconnected {
		t.Fatalf("a silent model disconnected the run: %+v", run)
	}
}
