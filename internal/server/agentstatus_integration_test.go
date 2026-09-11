//go:build integration

package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// The status reporter's whole path, in a real container: the settings file
// the server wrote into the run's coordination directory, the argument
// pointing the harness at it, the staged binary executing
// "aether-server report claude" against the socket its own mount carries,
// and the run moving between Working and Needs you as the agent says so.
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
printf '{"hook_event_name":"Stop"}' | ` + mcpbridge.BinaryPath + ` report claude
echo "reported:stop"
while read line; do
  printf '{"hook_event_name":"UserPromptSubmit"}' | ` + mcpbridge.BinaryPath + ` report claude
  echo "reported:prompt"
done
`

func TestIntegrationAgentStatusReporterInContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("the agent status reporter scenario needs a reachable Docker daemon")
	}
	image, _ := buildStatusAgentImage(t)
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
	att.waitOutput(t, "--settings "+path.Join(mcpbridge.MountDir, agentstatus.ClaudeSettingsName))
	att.waitOutput(t, "reported:stop")

	// The report itself: the run parks the moment the agent says its turn
	// ended, with the reason a member reads, and nothing like a stall
	// threshold has passed.
	parked := waitEvent(t, sub, &seen, "run.status needs-attention", func(ev events.Event) bool {
		p, isStatus := ev.Payload.(events.RunStatusPayload)
		return isStatus && ev.RunID == domain.RunID(run.ID) && p.To == domain.RunNeedsAttention
	})
	if p := parked.Payload.(events.RunStatusPayload); p.Reason != agentstatus.ReasonInput {
		t.Fatalf("park reason = %q, want %q", p.Reason, agentstatus.ReasonInput)
	}
	// The shipped threshold is ten minutes and this test's own budget is
	// three, so anything that lands here was the agent talking.
	if waited := time.Since(started); waited > time.Minute {
		t.Errorf("the run took %s to park; that is the silence heuristic, not the reporter", waited)
	}

	// And it comes back on the agent's own next turn, not on the steer.
	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: run.ID, Message: "keep going",
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
}

// buildStatusAgentImage builds the run image this scenario launches:
// busybox, the scripted agent installed as the "claude" executable the
// shipped profile launches, and a non-root user - the same user the
// container coordination scenario needs, for the same reason.
func buildStatusAgentImage(t *testing.T) (image, user string) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "claude"), statusAgentScript)
	if err := os.Chmod(filepath.Join(dir, "claude"), 0o755); err != nil {
		t.Fatalf("chmod the scripted agent: %v", err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 1000, 1000
	}
	user = fmt.Sprintf("%d:%d", uid, gid)
	writeFile(t, filepath.Join(dir, "Dockerfile"),
		"FROM busybox\nCOPY claude /usr/local/bin/claude\nUSER "+user+"\n")
	image = fmt.Sprintf("aether-e2e-statusagent:%d", os.Getpid())
	if out, err := exec.Command("docker", "build", "-q", "-t", image, dir).CombinedOutput(); err != nil {
		t.Fatalf("docker build %s: %v (%s)", image, err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rmi", "-f", image).CombinedOutput(); err != nil {
			t.Logf("remove image %s: %v (%s)", image, err, out)
		}
	})
	return image, user
}
