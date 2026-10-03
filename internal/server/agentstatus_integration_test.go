//go:build integration

package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// The status reporter's whole path, in a real container: the settings file
// the server wrote into the run's coordination directory, the argument
// pointing the harness at it, the staged binary executing
// "aether-server report claude" against the socket its own mount carries,
// and the run moving between Working and Idle as the agent says so.
//
// It runs on the shipped claude profile with a scripted stand-in for the
// CLI, so nothing here is a test-only branch in production code: the
// profile the operator gets is the profile under test.

// statusAgentScript stands in for Claude Code. It prints the argv it was
// launched with, reports the end of a turn exactly as the Stop hook would,
// and then waits - reporting a new turn whenever a steer reaches its
// stdin. Reporter failures reach stderr, which is the same PTY the test
// reads, so a broken path is visible rather than silent.
//
// It opens with a second of quiet, like every other scripted agent here:
// the PTY session and the run's own transition to running both land after
// the container starts, and a real agent takes far longer than this to
// finish a turn.
const statusAgentScript = `#!/bin/sh
sleep 1
echo "argv:$*"
printf '{"hook_event_name":"Stop"}' | ` + coordtransport.BinaryPath + ` report claude
echo "reported:stop"
while read line; do
  printf '{"hook_event_name":"UserPromptSubmit"}' | ` + coordtransport.BinaryPath + ` report claude
  echo "reported:prompt"
done
`

// piStatusAgentScript stands in for pi and omp, whose shared extension
// sends canonical execution/input JSON. It ends a turn, then starts a new
// one whenever a steer arrives.
const piStatusAgentScript = `#!/bin/sh
sleep 1
echo "argv:$*"
` + coordtransport.BinaryPath + ` report pi --json '{"state":"waiting","reason":"agent idle"}'
echo "reported:end"
while read line; do
  ` + coordtransport.BinaryPath + ` report pi --json '{"state":"working"}'
  echo "reported:start"
done
`

func TestIntegrationAgentStatusReporterInContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("the agent status reporter scenario needs a reachable Docker daemon")
	}
	image := buildStatusAgentImage(t, map[string]string{"claude": statusAgentScript, "pi": piStatusAgentScript})
	docker, _, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("the Docker daemon went away after the image was built")
	}

	e := &coordEnv{
		rt:           docker,
		image:        image,
		serverBinary: buildServerBinary(t),
		dataDir:      filepath.Join(shortTempDir(t), "data"),
	}
	srv := e.seed(ctx, t, false)
	sub := srv.subscribe(ctx, t)
	var seen []events.Event
	ctrl, client := srv.control(t, e.ada.key)

	started := time.Now()
	run := e.launch(t, ctrl, "report on yourself", "claude")
	att := openAttach(t, client, run.ID)

	// The launch wiring: the settings document is in the run's own
	// coordination directory, read-only like every other asset there, and
	// the harness was pointed at it inside the container.
	settings := filepath.Join(e.coordDir(run.ID), agentstatus.ClaudeSettingsName)
	info, err := os.Lstat(settings)
	if err != nil {
		t.Fatalf("the server wrote no %s for run %s: %v", agentstatus.ClaudeSettingsName, run.ID, err)
	}
	if got := info.Mode().Perm(); got != 0o444 {
		t.Errorf("%s mode = %o, want 0444", agentstatus.ClaudeSettingsName, got)
	}
	att.waitOutput(t, "--settings "+path.Join(coordtransport.MountDir, agentstatus.ClaudeSettingsName))
	att.waitOutput(t, "reported:stop")

	// The report itself: the run parks the moment the agent says its turn
	// ended, with the reason a member reads, and nothing like a stall
	// threshold has passed.
	parked := waitEvent(t, sub, &seen, "run.status needs-attention", func(ev events.Event) bool {
		p, isStatus := ev.Payload.(events.RunStatusPayload)
		return isStatus && ev.RunID == domain.RunID(run.ID) && p.To == domain.RunNeedsAttention
	})
	if p := parked.Payload.(events.RunStatusPayload); p.Reason != agentstatus.ReasonIdle {
		t.Fatalf("park reason = %q, want %q", p.Reason, agentstatus.ReasonIdle)
	}
	// The shipped threshold is ten minutes and this test's own budget is
	// three, so anything that lands here was the agent talking.
	if waited := time.Since(started); waited > time.Minute {
		t.Errorf("the run took %s to park; that is the silence heuristic, not the reporter", waited)
	}

	// And it comes back on the agent's own next turn, not on the steer.
	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: run.ID, Message: "keep going", IdempotencyKey: "agentstatus-keep-going-1",
	}, nil); err != nil {
		t.Fatalf("run.inject: %v", err)
	}
	att.waitOutput(t, "reported:prompt")
	resumed := waitEvent(t, sub, &seen, "run.status back to running", func(ev events.Event) bool {
		p, isStatus := ev.Payload.(events.RunStatusPayload)
		return isStatus && ev.RunID == domain.RunID(run.ID) &&
			p.From == domain.RunNeedsAttention && p.To == domain.RunRunning
	})
	if p := resumed.Payload.(events.RunStatusPayload); p.Reason != agentstatus.ReasonResumed {
		t.Fatalf("resume reason = %q, want %q", p.Reason, agentstatus.ReasonResumed)
	}

	if out := att.output(); strings.Contains(out, "aether-server report") {
		t.Errorf("the reporter wrote an error into the agent's terminal: %q", out)
	}

	// The second reporter shape, on the same server: pi and omp load one
	// extension file and name the event on the command line. Nothing about
	// the path differs, which is the point - the harness profile decides
	// what is written and what is appended, and the socket takes it from
	// there.
	piStarted := time.Now()
	piRun := e.launch(t, ctrl, "report on yourself", "pi")
	piAtt := openAttach(t, client, piRun.ID)
	extension := filepath.Join(e.coordDir(piRun.ID), agentstatus.PiExtensionName)
	piInfo, err := os.Lstat(extension)
	if err != nil {
		t.Fatalf("the server wrote no %s for run %s: %v", agentstatus.PiExtensionName, piRun.ID, err)
	}
	if got := piInfo.Mode().Perm(); got != 0o444 {
		t.Errorf("%s mode = %o, want 0444", agentstatus.PiExtensionName, got)
	}
	piAtt.waitOutput(t, "-e "+path.Join(coordtransport.MountDir, agentstatus.PiExtensionName))
	piAtt.waitOutput(t, "reported:end")

	piParked := waitEvent(t, sub, &seen, "pi run.status needs-attention", func(ev events.Event) bool {
		p, isStatus := ev.Payload.(events.RunStatusPayload)
		return isStatus && ev.RunID == domain.RunID(piRun.ID) && p.To == domain.RunNeedsAttention
	})
	if p := piParked.Payload.(events.RunStatusPayload); p.Reason != agentstatus.ReasonIdle {
		t.Fatalf("pi park reason = %q, want %q", p.Reason, agentstatus.ReasonIdle)
	}
	if waited := time.Since(piStarted); waited > time.Minute {
		t.Errorf("the pi run took %s to park; that is the silence heuristic, not the reporter", waited)
	}

	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: piRun.ID, Message: "keep going", IdempotencyKey: "agentstatus-keep-going-2",
	}, nil); err != nil {
		t.Fatalf("run.inject on the pi run: %v", err)
	}
	piAtt.waitOutput(t, "reported:start")
	piResumed := waitEvent(t, sub, &seen, "pi run.status back to running", func(ev events.Event) bool {
		p, isStatus := ev.Payload.(events.RunStatusPayload)
		return isStatus && ev.RunID == domain.RunID(piRun.ID) &&
			p.From == domain.RunNeedsAttention && p.To == domain.RunRunning
	})
	if p := piResumed.Payload.(events.RunStatusPayload); p.Reason != agentstatus.ReasonResumed {
		t.Fatalf("pi resume reason = %q, want %q", p.Reason, agentstatus.ReasonResumed)
	}
}

// buildStatusAgentImage builds the run image one of these scenarios
// launches: busybox, one scripted agent per reporter shape installed under
// the executable name the shipped profile launches, and a non-root user -
// the same user the container coordination scenario needs, for the same
// reason. The executables it carries also name the image, so two scenarios
// never build over each other.
func buildStatusAgentImage(t *testing.T, agents map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	executables := make([]string, 0, len(agents))
	for exe, script := range agents {
		writeFile(t, filepath.Join(dir, exe), script)
		if err := os.Chmod(filepath.Join(dir, exe), 0o755); err != nil {
			t.Fatalf("chmod the scripted %s agent: %v", exe, err)
		}
		executables = append(executables, exe)
	}
	slices.Sort(executables)
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 1000, 1000
	}
	user := fmt.Sprintf("%d:%d", uid, gid)
	writeFile(t, filepath.Join(dir, "Dockerfile"),
		"FROM busybox\nCOPY "+strings.Join(executables, " ")+" /usr/local/bin/\nUSER "+user+"\n")
	image := fmt.Sprintf("aether-e2e-statusagent-%s:%d", strings.Join(executables, "-"), os.Getpid())
	if out, err := exec.Command("docker", "build", "-q", "-t", image, dir).CombinedOutput(); err != nil {
		t.Fatalf("docker build %s: %v (%s)", image, err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rmi", "-f", image).CombinedOutput(); err != nil {
			t.Logf("remove image %s: %v (%s)", image, err, out)
		}
	})
	return image
}

// The scripted CLI reports through the same run-scoped socket as the plugin.
const openCodeAgentScript = `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "1.18.32"
  exit 0
fi
sleep 1
` + coordtransport.BinaryPath + ` report opencode --json '{"state":"waiting","reason":"agent idle"}'
echo "reported:idle"
while read line; do
  ` + coordtransport.BinaryPath + ` report opencode --json '{"state":"working"}'
  echo "reported:busy"
done
`

// Exercise the native launch wrapper and status transitions in a real container.
func TestIntegrationOpenCodeStatusReporterInContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("the opencode status reporter scenario needs a reachable Docker daemon")
	}
	image := buildStatusAgentImage(t, map[string]string{"opencode": openCodeAgentScript})
	docker, _, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("the Docker daemon went away after the image was built")
	}

	e := &coordEnv{
		rt:           docker,
		image:        image,
		serverBinary: buildServerBinary(t),
		dataDir:      filepath.Join(shortTempDir(t), "data"),
	}
	srv := e.seed(ctx, t, false)
	sub := srv.subscribe(ctx, t)
	var seen []events.Event
	ctrl, client := srv.control(t, e.ada.key)

	started := time.Now()
	run := e.launch(t, ctrl, "report on yourself", "opencode")
	att := openAttach(t, client, run.ID)

	plugin := filepath.Join(e.coordDir(run.ID), agentstatus.OpenCodePluginName)
	info, err := os.Lstat(plugin)
	if err != nil {
		t.Fatalf("the server wrote no %s for run %s: %v", agentstatus.OpenCodePluginName, run.ID, err)
	}
	if got := info.Mode().Perm(); got != 0o444 {
		t.Errorf("%s mode = %o, want 0444", agentstatus.OpenCodePluginName, got)
	}
	att.waitOutput(t, "reported:idle")

	parked := waitEvent(t, sub, &seen, "run.status needs-attention", func(ev events.Event) bool {
		p, isStatus := ev.Payload.(events.RunStatusPayload)
		return isStatus && ev.RunID == domain.RunID(run.ID) && p.To == domain.RunNeedsAttention
	})
	if p := parked.Payload.(events.RunStatusPayload); p.Reason != agentstatus.ReasonIdle {
		t.Fatalf("park reason = %q, want %q", p.Reason, agentstatus.ReasonIdle)
	}
	if waited := time.Since(started); waited > time.Minute {
		t.Errorf("the run took %s to park; that is the silence heuristic, not the reporter", waited)
	}

	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: run.ID, Message: "keep going", IdempotencyKey: "agentstatus-keep-going-3",
	}, nil); err != nil {
		t.Fatalf("run.inject: %v", err)
	}
	att.waitOutput(t, "reported:busy")
	resumed := waitEvent(t, sub, &seen, "run.status back to running", func(ev events.Event) bool {
		p, isStatus := ev.Payload.(events.RunStatusPayload)
		return isStatus && ev.RunID == domain.RunID(run.ID) &&
			p.From == domain.RunNeedsAttention && p.To == domain.RunRunning
	})
	if p := resumed.Payload.(events.RunStatusPayload); p.Reason != agentstatus.ReasonResumed {
		t.Fatalf("resume reason = %q, want %q", p.Reason, agentstatus.ReasonResumed)
	}

	if out := att.output(); strings.Contains(out, "aether-server report") {
		t.Errorf("the reporter wrote an error into the agent's terminal: %q", out)
	}
}

// The assembled server receives real run.report RPCs over the run's Unix
// coordination socket; SSH snapshots and durable replay must agree without
// conflating an unresolved request with the agent's execution state.
func TestIntegrationRunInputReports(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")
	e := &coordEnv{
		rt:      newE2ERuntime(),
		image:   "e2e/fake",
		dataDir: filepath.Join(shortTempDir(t), "data"),
	}
	srv := e.seed(ctx, t, false)
	release := make(chan struct{})
	defer close(release)
	const task = "independent input reports"
	e.e2e(t).script(task, func(c *e2eContainer) {
		coordAgent{release: release}.run(ctx, c)
	})
	ctrl, client := srv.control(t, e.ada.key)
	defer client.Close()
	sub := srv.subscribe(ctx, t)
	var seen []events.Event
	run := e.launch(t, ctrl, task, "fake")
	socket := waitMissionSocket(t, e.coordDir(run.ID))

	question := domain.RunInputRequest{ID: "request-1", SessionID: "session-a", Kind: "question"}
	permission := domain.RunInputRequest{ID: question.ID, SessionID: question.SessionID, Kind: "permission"}
	otherSession := domain.RunInputRequest{ID: question.ID, SessionID: "session-b", Kind: question.Kind}
	update := func(operation string, request domain.RunInputRequest) domain.RunInputUpdate {
		return domain.RunInputUpdate{
			Operation: operation, ID: request.ID, SessionID: request.SessionID, Kind: request.Kind,
		}
	}
	sameInputs := func(got, want []domain.RunInputRequest) bool {
		if len(got) != len(want) {
			return false
		}
		for _, request := range want {
			if !slices.Contains(got, request) {
				return false
			}
		}
		return true
	}
	assertSnapshots := func(t *testing.T, status domain.RunStatus, pending []domain.RunInputRequest) {
		t.Helper()
		assertRun := func(source string, got protocol.Run) {
			t.Helper()
			if got.Status != string(status) || got.PendingInputs == nil || !sameInputs(got.PendingInputs, pending) {
				t.Fatalf("%s snapshot = status %q, inputs %+v; want %q, %+v (non-null list)",
					source, got.Status, got.PendingInputs, status, pending)
			}
		}
		var got protocol.RunResult
		if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: run.ID}, &got); err != nil {
			t.Fatalf("run.get: %v", err)
		}
		assertRun("run.get", got.Run)
		var listed protocol.RunListResult
		if err := ctrl.Call(protocol.MethodRunList, protocol.RunListParams{WorkspaceID: run.WorkspaceID}, &listed); err != nil {
			t.Fatalf("run.list: %v", err)
		}
		for _, candidate := range listed.Runs {
			if candidate.ID == run.ID {
				assertRun("run.list", candidate)
				return
			}
		}
		t.Fatalf("run.list omitted %s", run.ID)
	}
	steps := []struct {
		name    string
		report  protocol.RunReportParams
		pending []domain.RunInputRequest
	}{
		{
			name: "working with independent pending inputs",
			report: protocol.RunReportParams{
				State: string(agentstatus.Working),
				InputUpdates: []domain.RunInputUpdate{
					update("open", question), update("open", permission),
				},
			},
			pending: []domain.RunInputRequest{question, permission},
		},
		{
			name:    "input-only open preserves working execution",
			report:  protocol.RunReportParams{InputUpdates: []domain.RunInputUpdate{update("open", otherSession)}},
			pending: []domain.RunInputRequest{question, permission, otherSession},
		},
		{
			name:    "close matches kind as well as session and id",
			report:  protocol.RunReportParams{InputUpdates: []domain.RunInputUpdate{update("close", permission)}},
			pending: []domain.RunInputRequest{question, otherSession},
		},
		{
			name:    "close preserves another session's same id",
			report:  protocol.RunReportParams{InputUpdates: []domain.RunInputUpdate{update("close", question)}},
			pending: []domain.RunInputRequest{otherSession},
		},
		{
			name:    "last close clears input without changing execution",
			report:  protocol.RunReportParams{InputUpdates: []domain.RunInputUpdate{update("close", otherSession)}},
			pending: []domain.RunInputRequest{},
		},
	}
	var inputEvents []events.Event
	var lastSeq uint64
	for index, step := range steps {
		if !t.Run(step.name, func(t *testing.T) {
			if err := coordtransport.Call(ctx, socket, protocol.MethodRunReport, step.report, nil); err != nil {
				t.Fatalf("run.report: %v", err)
			}
			ev := waitEvent(t, sub, &seen, step.name, func(ev events.Event) bool {
				return ev.RunID == domain.RunID(run.ID) && ev.Type == events.TypeRunInput && ev.Seq > lastSeq
			})
			p, ok := ev.Payload.(events.RunInputPayload)
			if !ok || p.PendingInputs == nil || !sameInputs(p.PendingInputs, step.pending) {
				t.Fatalf("run.input payload = %+v, want %+v", ev.Payload, step.pending)
			}
			lastSeq = ev.Seq
			inputEvents = append(inputEvents, ev)
			assertSnapshots(t, domain.RunRunning, step.pending)
			if index == 0 {
				// Mutation responses seed the same client cache as get/list.
				// Protecting a Working run must not erase its pending requests.
				var protected protocol.RunResult
				if err := ctrl.Call(protocol.MethodRunProtect, protocol.RunProtectParams{RunID: run.ID, Protected: true}, &protected); err != nil {
					t.Fatalf("run.protect: %v", err)
				}
				if !protected.Run.Protected || protected.Run.Status != string(domain.RunRunning) || !sameInputs(protected.Run.PendingInputs, step.pending) {
					t.Fatalf("protection response erased execution/input state: %+v", protected.Run)
				}
			}
		}) {
			return
		}
	}
	// A repeated close is idempotent, and an idle report is not an input
	// request. The status event is a durable barrier for replay below.
	if err := coordtransport.Call(ctx, socket, protocol.MethodRunReport, protocol.RunReportParams{
		InputUpdates: []domain.RunInputUpdate{update("close", otherSession)},
	}, nil); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	if err := coordtransport.Call(ctx, socket, protocol.MethodRunReport, protocol.RunReportParams{
		State: string(agentstatus.Idle), Reason: agentstatus.ReasonIdle,
	}, nil); err != nil {
		t.Fatalf("idle report: %v", err)
	}
	idle := waitEvent(t, sub, &seen, "idle without pending inputs", func(ev events.Event) bool {
		p, ok := ev.Payload.(events.RunStatusPayload)
		return ok && ev.RunID == domain.RunID(run.ID) && p.To == domain.RunNeedsAttention && ev.Seq > lastSeq
	})
	assertSnapshots(t, domain.RunNeedsAttention, []domain.RunInputRequest{})

	replay, err := srv.srv.Bus().Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Run: domain.RunID(run.ID)}, Replay: true, Buffer: 128,
	})
	if err != nil {
		t.Fatalf("subscribe durable replay: %v", err)
	}
	defer replay.Close()
	var replayed []events.Event
	waitEvent(t, replay, &replayed, "durable idle barrier", func(ev events.Event) bool {
		return ev.Seq == idle.Seq
	})
	var replayedInputs []events.Event
	for _, ev := range replayed {
		if ev.Type == events.TypeRunInput {
			replayedInputs = append(replayedInputs, ev)
		}
	}
	if len(replayedInputs) != len(inputEvents) {
		t.Fatalf("durable input events = %d, want %d (no event for duplicate close or idle)",
			len(replayedInputs), len(inputEvents))
	}
	for i, ev := range replayedInputs {
		p, ok := ev.Payload.(events.RunInputPayload)
		if ev.Seq != inputEvents[i].Seq || !ok || p.PendingInputs == nil || !sameInputs(p.PendingInputs, steps[i].pending) {
			t.Fatalf("durable input event %d = %+v, want seq %d, inputs %+v",
				i, ev, inputEvents[i].Seq, steps[i].pending)
		}
	}
}
