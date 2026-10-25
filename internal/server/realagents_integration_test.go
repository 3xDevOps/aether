//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	realAgentKey     = "aether-mock-model-api-key"
	realAgentCommand = "echo aether-$((40+2))"
	realAgentOutput  = "aether-42"
	realAgentAnswer  = "aether-turn-done"
)

var realAgentModes = map[domain.LaunchMode]string{domain.LaunchTUI: "standard", domain.LaunchACP: "enhanced"}

func TestIntegrationRealAgents(t *testing.T) {
	image := os.Getenv("AETHER_AGENTS_IMAGE")
	if image == "" {
		t.Skip("AETHER_AGENTS_IMAGE unset; build the image with make agents-image, then run make test-agents (docs/testing.md)")
	}
	if !dockerReachable(t) {
		t.Fatal("AETHER_AGENTS_IMAGE is set but the Docker daemon is unreachable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// dockerRuntime's containers have no network, and these must reach the
	// mock model.
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
	e := &coordEnv{
		rt:           docker,
		image:        image,
		serverBinary: buildServerBinary(t),
		dataDir:      filepath.Join(shortTempDir(t), "data"),
	}
	srv := e.seed(ctx, t, false)
	ctrl, sshClient := srv.control(t, e.ada.key)
	sender := e.launch(t, ctrl, "send mail to the agents under test", "fake")
	senderSocket := waitMissionSocket(t, e.coordDir(sender.ID))

	// The sender's launch created the agent network, whose gateway is the
	// host as run containers reach it.
	network, err := cli.NetworkInspect(ctx, runtime.AgentNetwork, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatalf("inspect %s: %v", runtime.AgentNetwork, err)
	}
	if len(network.Network.IPAM.Config) == 0 {
		t.Fatalf("%s has no IPAM config", runtime.AgentNetwork)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(network.Network.IPAM.Config[0].Gateway.String(), "0"))
	if err != nil {
		t.Fatalf("listen for the mock model: %v", err)
	}
	model := &mockModel{command: realAgentCommand, answer: realAgentAnswer}
	modelServer := &httptest.Server{Listener: ln, Config: &http.Server{Handler: model}}
	modelServer.Start()
	t.Cleanup(modelServer.Close)
	t.Setenv("ANTHROPIC_API_KEY", realAgentKey)
	t.Setenv("ANTHROPIC_BASE_URL", modelServer.URL)

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
		".codex/auth.json":      `{"auth_mode": "apikey", "OPENAI_API_KEY": "` + realAgentKey + `"}`,
		".codex/config.toml": fmt.Sprintf("openai_base_url = %q\nmodel = \"mock-model\"\ncheck_for_update_on_startup = false\n\n[projects.%q]\ntrust_level = \"trusted\"\n",
			modelServer.URL+"/v1", domain.PrimaryRepoPath),
	} {
		path := filepath.Join(e.dataDir, "homes", string(e.ada.id), name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("agents", func(t *testing.T) {
		for _, agent := range []string{"claude", "codex"} {
			for mode, modeName := range realAgentModes {
				t.Run(agent+"-"+modeName, func(t *testing.T) {
					t.Parallel()
					a := &realAgentRun{srv: srv, ctrl: ctrl, model: model, agent: agent, mode: mode, marker: "real-agent-" + agent + "-" + modeName}
					var launched protocol.RunLaunchResult
					if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
						WorkspaceID: string(e.ws.ID), Task: a.marker + ": run the command, then stop.",
						Harness: agent, Mode: string(mode),
					}, &launched); err != nil {
						t.Fatalf("run.launch %s %s: %v", agent, mode, err)
					}
					a.run = launched.Run
					if mode == domain.LaunchTUI {
						a.observer = openMissionObserver(t, sshClient, a.run.ID)
					}

					if !t.Run("turn", func(t *testing.T) {
						a.waitFor(ctx, t, "the tool call's output", func() bool { return model.saw(a.marker, realAgentOutput) })
						a.waitIdle(ctx, t, 0)
						a.waitFor(ctx, t, "the model's answer in the run", func() bool { return strings.Contains(a.transcript(), realAgentAnswer) })
					}) {
						return
					}

					t.Run("wake", func(t *testing.T) {
						if err := harness.SwarmRefusal(agent, mode); err != nil {
							t.Skipf("Aether cannot wake this run: %v", err)
						}
						seq := a.get(t).StatusSeq
						if err := pacedCall(ctx, senderSocket, protocol.MethodCoordSend, protocol.CoordSendParams{
							ToRunID: a.run.ID, Body: "this mail only has to wake you", IdempotencyKey: "wake-" + a.marker,
						}, nil); err != nil {
							t.Fatalf("coord.send to %s: %v", a.run.ID, err)
						}
						prompt, _, _ := strings.Cut(protocol.CoordInboxContext(1), ".")
						a.waitFor(ctx, t, "a turn prompted by the mail", func() bool { return model.saw(a.marker, prompt) })
						a.waitIdle(ctx, t, seq)
					})
				})
			}
		}
	})
}

type realAgentRun struct {
	srv    *coordServer
	ctrl   *protocol.Client
	model  *mockModel
	agent  string
	mode   domain.LaunchMode
	marker string

	run      protocol.Run
	observer *attachConn
}

func (a *realAgentRun) get(t *testing.T) protocol.Run {
	t.Helper()
	var res protocol.RunResult
	if err := a.ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: a.run.ID}, &res); err != nil {
		t.Fatalf("run.get %s: %v", a.run.ID, err)
	}
	return res.Run
}

func (a *realAgentRun) waitIdle(ctx context.Context, t *testing.T, after int64) {
	t.Helper()
	a.waitFor(ctx, t, "the run to settle idle", func() bool {
		r := a.get(t)
		return r.StatusSeq > after && r.Status == string(domain.RunNeedsAttention) && domain.RunActivity(r.Activity) == domain.ActivityWaiting
	})
}

func (a *realAgentRun) waitFor(ctx context.Context, t *testing.T, what string, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for !done() {
		select {
		case <-ctx.Done():
			shown := a.transcript()
			t.Fatalf("%s %s: no %s: %v\nrun: %+v\nmodel requests:%s\nshown:\n%s", a.agent, realAgentModes[a.mode], what, ctx.Err(),
				a.get(t), a.model.tails(a.marker), shown[max(0, len(shown)-4000):])
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// A TUI moves the cursor between words instead of printing spaces, so
// whitespace goes with the control sequences.
var terminalControl = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]|\s`)

func (a *realAgentRun) transcript() string {
	if a.observer != nil {
		return terminalControl.ReplaceAllString(a.observer.output(), "")
	}
	page, err := a.srv.srv.sched.ACPHistory(domain.RunID(a.run.ID), 0, 0)
	if err != nil {
		return "ACP history: " + err.Error()
	}
	var b strings.Builder
	for _, it := range page.Items {
		fmt.Fprintf(&b, "%s %s", it.Kind, it.StopReason)
		if it.Message != nil {
			b.WriteString(" " + it.Message.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
