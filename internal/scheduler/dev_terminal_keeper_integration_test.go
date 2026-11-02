//go:build integration

package scheduler

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestIntegrationDevelopmentTerminalSurvivesSchedulerRestart restarts the
// scheduler while a development terminal runs in a real container: the same
// process keeps running, and after the restart its output continues from the
// earlier cursor, input and resize reach it, and stop ends it.
func TestIntegrationDevelopmentTerminalSurvivesSchedulerRestart(t *testing.T) {
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	server := buildStatic(t, "./cmd/aether-server", "aether-server")
	newHost := func() *ptyhost.Host {
		host, hostErr := ptyhost.New(ptyhost.Config{TranscriptDir: t.TempDir()})
		if hostErr != nil {
			t.Fatal(hostErr)
		}
		t.Cleanup(func() { _ = host.Close() })
		return host
	}
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = docker
		cfg.ServerBinary = server
		cfg.PTY = newHost()
		cfg.Control = control.New(control.Config{})
		cfg.Harnesses = map[string]HarnessSpec{"fake": {TUIArgs: []string{"sh", "-c", "sleep 3600"}}}
	})
	// Coordination stages the server binary, the keeper, into the container.
	e.sched.UseCoordination(&fakeCoordinator{root: t.TempDir()}, t.TempDir(), false)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "keep a terminal", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	container := sc.ContainerID
	t.Cleanup(func() { _ = docker.Destroy(context.Background(), runtime.ID(container)) })

	principal := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	call := func(s *Scheduler, method string, params, result any) {
		t.Helper()
		raw, marshalErr := json.Marshal(params)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		got, callErr := s.DevelopmentTerminal(ctx, run.ID, principal, method, raw, func() error { return nil })
		if callErr != nil {
			t.Fatalf("%s: %v", method, callErr)
		}
		out, _ := json.Marshal(got)
		if decodeErr := json.Unmarshal(out, result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
	}
	ps := func() string {
		t.Helper()
		out, psErr := exec.CommandContext(ctx, "docker", "exec", container, "ps", "-o", "pid,args").CombinedOutput()
		if psErr != nil {
			t.Fatalf("docker exec ps: %v\n%s", psErr, out)
		}
		return string(out)
	}
	script := `trap 'echo got-int' INT; echo "user:$(id -u):$HOME"; echo started; sleep 3; echo while-down; while read l; do echo "got:$l"; [ "$l" = size ] && stty size; [ "$l" = nap ] && sleep 60; done`
	var started protocol.DevTerminalStartResult
	call(e.sched, protocol.MethodDevTerminalStart, protocol.DevTerminalStartParams{Name: "job", Command: []string{"/bin/sh", "-c", script}, Cols: 80, Rows: 24}, &started)
	target := protocol.DevTerminalTarget{TerminalID: started.Terminal.TerminalID, Incarnation: started.Terminal.Incarnation}
	var waited protocol.DevTerminalWaitResult
	call(e.sched, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{DevTerminalTarget: target, Contains: "started", TimeoutMS: 10000}, &waited)
	if !waited.Matched {
		t.Fatalf("the terminal never started: %+v", waited)
	}
	cursor := waited.Position
	// The keeper runs as root, yet the command gets the user and HOME a
	// plain exec of the container gets.
	plain, err := exec.CommandContext(ctx, "docker", "exec", container, "sh", "-c", `echo "user:$(id -u):$HOME"`).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec: %v\n%s", err, plain)
	}
	var early protocol.DevTerminalOutputResult
	call(e.sched, protocol.MethodDevTerminalOutput, protocol.DevTerminalOutputParams{DevTerminalTarget: target}, &early)
	if want := strings.TrimSpace(string(plain)); !strings.Contains(early.Text, want+"\r\n") {
		t.Fatalf("the terminal's identity = %q, want %q", early.Text, want)
	}
	before := ps()
	if !strings.Contains(before, "acp-keeper term") {
		t.Fatalf("the terminal runs without a keeper:\n%s", before)
	}

	// The scheduler stops as a server process does; the command writes
	// while no server is attached.
	if err = e.sched.DetachDevelopmentTerminals(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := e.sched.cfg
	cfg.PTY = newHost()
	recovered, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	time.Sleep(4 * time.Second)
	if err = recovered.RecoverDevelopmentTerminals(ctx, run.ID); err != nil {
		t.Fatal(err)
	}

	var output protocol.DevTerminalOutputResult
	call(recovered, protocol.MethodDevTerminalOutput, protocol.DevTerminalOutputParams{DevTerminalTarget: target, After: &cursor}, &output)
	if output.MissingCursor || output.Truncated || output.Terminal.Process.State != "running" || !strings.Contains(output.Text, "while-down") {
		t.Fatalf("output after the pre-restart cursor = %+v", output)
	}
	var fence protocol.DevControlFence
	lease, _, err := cfg.Control.AcquireSurface(string(run.ID), control.Surface{Kind: control.SurfaceTerminal, ID: target.TerminalID, Incarnation: target.Incarnation},
		principal, "integration", false, 0, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	fence = protocol.DevControlFence{ControlSessionID: lease.SessionID, ControlGeneration: lease.Generation}
	var input protocol.DevTerminalInputResult
	call(recovered, protocol.MethodDevTerminalInput, protocol.DevTerminalInputParams{DevTerminalTarget: target, DevControlFence: fence, Kind: "text", Text: "hello\r"}, &input)
	call(recovered, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{DevTerminalTarget: target, Contains: "got:hello", TimeoutMS: 10000}, &waited)
	if !input.Accepted || !waited.Matched {
		t.Fatalf("input after restart: accepted %v, wait %+v", input.Accepted, waited)
	}
	var resized protocol.DevTerminalResizeResult
	call(recovered, protocol.MethodDevTerminalResize, protocol.DevTerminalResizeParams{DevTerminalTarget: target, DevControlFence: fence, Cols: 100, Rows: 30}, &resized)
	call(recovered, protocol.MethodDevTerminalInput, protocol.DevTerminalInputParams{DevTerminalTarget: target, DevControlFence: fence, Kind: "text", Text: "size\r"}, &input)
	call(recovered, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{DevTerminalTarget: target, Contains: "30 100", TimeoutMS: 10000}, &waited)
	if !waited.Matched {
		t.Fatalf("the program did not see the resize: %+v", waited)
	}
	call(recovered, protocol.MethodDevTerminalInput, protocol.DevTerminalInputParams{DevTerminalTarget: target, DevControlFence: fence, Kind: "text", Text: "nap\r"}, &input)
	call(recovered, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{DevTerminalTarget: target, Contains: "got:nap", TimeoutMS: 10000}, &waited)
	call(recovered, protocol.MethodDevTerminalInput, protocol.DevTerminalInputParams{DevTerminalTarget: target, DevControlFence: fence, Kind: "key", Key: "c", Modifiers: []string{"ctrl"}}, &input)
	call(recovered, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{DevTerminalTarget: target, Contains: "got-int", TimeoutMS: 10000}, &waited)
	if !waited.Matched {
		t.Fatalf("Ctrl-C did not interrupt the program: %+v", waited)
	}
	terminalProcesses := func(listing string) []string {
		var lines []string
		for line := range strings.Lines(listing) {
			if strings.Contains(line, "while read l") {
				lines = append(lines, strings.TrimSpace(line))
			}
		}
		return lines
	}
	if after := ps(); !slices.Equal(terminalProcesses(after), terminalProcesses(before)) {
		t.Fatalf("the restart replaced the terminal's processes:\nbefore\n%s\nafter\n%s", before, after)
	}

	var stopped protocol.DevTerminalStopResult
	call(recovered, protocol.MethodDevTerminalStop, protocol.DevTerminalStopParams{DevTerminalTarget: target, DevControlFence: fence}, &stopped)
	if !stopped.Stopped || stopped.Terminal.Process.State != "stopped" {
		t.Fatalf("stop after restart = %+v", stopped)
	}
	if after := ps(); strings.Contains(after, "acp-keeper term") || strings.Contains(after, "while read l") {
		t.Fatalf("stop left the terminal running:\n%s", after)
	}
}
