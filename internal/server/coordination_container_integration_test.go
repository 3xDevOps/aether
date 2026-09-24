//go:build integration

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// The container half of coordination, which the in-process scenarios in
// coordination_integration_test.go cannot reach: real bind mounts, the
// staged binary executed as /opt/aether/aether-server mcp, and a non-root
// container user traversing the coordination directory to the socket.
//
// Its failure mode is silent - a run that cannot reach the bridge degrades
// to notice-only, which is a legal state - so everything here is asserted
// positively, from two sides: the daemon's own view of the realized mounts,
// and the agent's report of what it found and did inside the container.

// TestIntegrationCoordinationInContainer launches two overlapping runs on
// the shipped claude profile in real containers and makes each agent settle
// the overlap through the staged bridge as the image's non-root user.
func TestIntegrationCoordinationInContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("the container coordination scenario needs a reachable Docker daemon")
	}
	// Before the runtime, so that its cleanup runs after the runtime's
	// container sweep: cleanups run last-in-first-out, and removing an
	// image a live container still holds only untags it, leaving the layers
	// behind on every run.
	image, user := buildCoordAgentImage(t)
	docker, _, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("the Docker daemon went away after the image was built")
	}

	e := &coordEnv{rt: docker, image: image, serverBinary: buildServerBinary(t)}
	srv := e.seed(ctx, t, false)
	sub := srv.subscribe(ctx, t)
	var seen []events.Event
	adaCtrl, adaClient := srv.control(t, e.ada.key)
	boCtrl, boClient := srv.control(t, e.bo.key)

	const taskA, taskB = "container coordinate A", "container coordinate B"
	runA := e.launch(t, adaCtrl, taskA, "claude")
	runB := e.launch(t, boCtrl, taskB, "claude")
	attA := openAttach(t, adaClient, runA.ID)
	attB := openAttach(t, boClient, runB.ID)

	// The daemon's side: two realized read-only binds carrying the staged
	// binary and the run's own coordination directory, into a container
	// running as the image's non-root user.
	cli := newDockerCLI(t)
	for _, run := range []protocol.Run{runA, runB} {
		e.assertRealizedMounts(ctx, t, cli, run.ID, user)
	}

	// The agent's side, reported from inside the container: the argument it
	// The fixture invokes the staged MCP bridge itself. No harness receives an
	// automatic config argument, but the optional manually invoked MCP path
	// remains a real round trip over the mounted socket.
	for _, att := range []*attachConn{attA, attB} {
		if strings.Contains(att.output(), "--mcp-config") {
			t.Errorf("run received obsolete automatic MCP config: %q", att.output())
		}
		att.waitOutput(t, "assets:manual-mcp")
		att.waitOutput(t, "user:"+user)
		att.waitOutput(t, "mode:"+coordtransport.MountDir+"=0755")
		att.waitOutput(t, coord.CoAuthorsName+"=0444")
		att.waitOutput(t, coordtransport.SocketName+"=0666")
		att.waitOutput(t, "mount:"+coordtransport.MountDir+"=ro")
		att.waitOutput(t, "readonly:"+coordtransport.MountDir)
		att.waitOutput(t, "mode:"+coordtransport.BinaryPath+"=0555")
		att.waitOutput(t, "mount:"+coordtransport.BinaryPath+"=ro")
		att.waitOutput(t, "bridge:"+coordtransport.BinaryPath+" mcp")
		att.waitOutput(t, "inbox:handled by ")
		att.waitOutput(t, "report:")
		if !strings.Contains(att.output(), `"ok":true`) {
			t.Errorf("manual MCP fixture report was not acknowledged: %q", att.output())
		}
	}

	// The round trip and final report are performed inside the real container,
	// through the manually invoked MCP bridge and mounted CLI.
	waitEvent(t, sub, &seen, "run A's coordination note", coordNote(runA.ID, runB.ID))
	waitEvent(t, sub, &seen, "run B's coordination note", coordNote(runB.ID, runA.ID))
	for _, att := range []*attachConn{attA, attB} {
		assertNoAgentError(t, att)
	}
}

// TestIntegrationCoordinationCLIFromShellHarnesses proves the alternate
// delivery path in the place it matters: two different shell-capable
// harnesses execute the staged server binary through the new in-container
// pathname, rather than talking to MCP or merely inspecting a mount spec.
func TestIntegrationCoordinationCLIFromShellHarnesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("the shell coordination scenario needs a reachable Docker daemon")
	}
	image, user := buildCoordAgentImage(t)
	docker, _, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("the Docker daemon went away after the image was built")
	}
	e := &coordEnv{
		rt: docker, image: image, serverBinary: buildServerBinary(t),
		dataDir: filepath.Join(shortTempDir(t), "data"),
	}
	srv := e.seed(ctx, t, false)
	sub := srv.subscribe(ctx, t)
	var seen []events.Event
	adaCtrl, adaClient := srv.control(t, e.ada.key)
	boCtrl, boClient := srv.control(t, e.bo.key)

	runA := e.launch(t, adaCtrl, "shell coordination A", "pi")
	attA := openAttach(t, adaClient, runA.ID)
	if _, err := attA.stdin.Write([]byte("aether-cli-start\r")); err != nil {
		t.Fatalf("start pi shell fixture: %v", err)
	}
	// A taskless terminal must still receive the CLI and skill workflow.
	runB := e.launch(t, boCtrl, "", "omp")
	attB := openAttach(t, boClient, runB.ID)
	if _, err := attB.stdin.Write([]byte("aether-cli-start\r")); err != nil {
		t.Fatalf("start omp shell fixture: %v", err)
	}
	cli := newDockerCLI(t)
	mountsOK := true
	for _, run := range []protocol.Run{runA, runB} {
		if !e.assertRealizedMounts(ctx, t, cli, run.ID, user) {
			mountsOK = false
			notes := collectCoordinationTimeline(t, sub, &seen, run.ID)
			t.Errorf("run %s coordination mounts unavailable; timeline notes: %v", run.ID, notes)
		}
	}
	if !mountsOK {
		return
	}
	digest := fileDigest(t, e.serverBinary)
	for _, att := range []*attachConn{attA, attB} {
		att.waitOutput(t, "cli-no-auto-mcp:")
		att.waitOutput(t, "cli-help:")
		att.waitOutput(t, "cli-skill-available:")
		att.waitOutput(t, "cli-digest:"+digest)
		att.waitOutput(t, "cli-status:")
		att.waitOutput(t, "cli-sent:")
		att.waitOutput(t, "cli-inbox:")
		att.waitOutput(t, "cli-acked:")
		att.waitOutput(t, "cli-reported:")
		assertNoAgentError(t, att)
	}
}

// assertRealizedMounts reads the three coordination binds back off the live
// container. The bridge source must be the staged copy of the very binary
// the server was told to stage, named by its own content hash.
func (e *coordEnv) assertRealizedMounts(ctx context.Context, t *testing.T, cli *client.Client, run, user string) bool {
	t.Helper()
	insp, err := cli.ContainerInspect(ctx, containerName(run), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect run %s's container: %v", run, err)
	}
	staged := filepath.Join(e.dataDir, "runtime", "bin", "aether-server-"+fileDigest(t, e.serverBinary))
	want := map[string]string{
		coordtransport.BinaryPath: resolved(t, staged),
		coordtransport.CLIPath:    resolved(t, staged),
		coordtransport.MountDir:   resolved(t, e.coordDir(run)),
	}
	realized := make(map[string]container.MountPoint, len(insp.Container.Mounts))
	for _, m := range insp.Container.Mounts {
		realized[m.Destination] = m
	}
	valid := true
	for target, source := range want {
		m, ok := realized[target]
		switch {
		case !ok:
			t.Errorf("run %s has no realized mount at %s: %+v", run, target, insp.Container.Mounts)
			valid = false
		case m.Source != source:
			t.Errorf("run %s mount %s comes from %q, want %q", run, target, m.Source, source)
			valid = false
		case m.RW:
			t.Errorf("run %s mount %s is writable", run, target)
			valid = false
		}
	}
	return valid
}

// assertDisabledCLIOnly inspects a live disabled run. The staged CLI remains
// a read-only digest-pinned bind, while the bridge and socket directory are
// absent from the actual container.
func (e *coordEnv) assertDisabledCLIOnly(ctx context.Context, t *testing.T, cli *client.Client, run, user string) {
	t.Helper()
	insp, err := cli.ContainerInspect(ctx, containerName(run), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect disabled run %s's container: %v", run, err)
	}
	if insp.Container.Config.User != user {
		t.Fatalf("disabled run %s user = %q, want %q", run, insp.Container.Config.User, user)
	}
	staged := filepath.Join(e.dataDir, "runtime", "bin", "aether-server-"+fileDigest(t, e.serverBinary))
	var cliMount *container.MountPoint
	for i := range insp.Container.Mounts {
		m := &insp.Container.Mounts[i]
		switch m.Destination {
		case coordtransport.CLIPath:
			cliMount = m
		case coordtransport.BinaryPath, coordtransport.MountDir:
			t.Errorf("disabled run %s unexpectedly has coordination mount %s", run, m.Destination)
		}
	}
	if cliMount == nil {
		t.Fatalf("disabled run %s has no CLI mount: %+v", run, insp.Container.Mounts)
	}
	want := resolved(t, staged)
	if cliMount.Source != want {
		t.Errorf("disabled run %s CLI source = %q, want %q", run, cliMount.Source, want)
	}
	if cliMount.RW {
		t.Errorf("disabled run %s CLI mount is writable", run)
	}
}

// collectCoordinationTimeline drains a short, bounded window after a mount
// assertion fails so an advisory provisioning error is not hidden behind the
// runtime's realized-mount report.
func collectCoordinationTimeline(t *testing.T, sub events.Subscription, seen *[]events.Event, run string) []string {
	t.Helper()
	var notes []string
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				return notes
			}
			*seen = append(*seen, ev)
			payload, ok := ev.Payload.(events.TimelinePayload)
			if ok && string(ev.RunID) == run && payload.Kind == events.TimelineNote &&
				strings.HasPrefix(payload.Message, "coordination unavailable for this run:") {
				notes = append(notes, payload.Message)
			}
		case <-timer.C:
			return notes
		}
	}
}

// TestIntegrationCoordinationCLIWhenDisabled proves that an ordinary
// taskless member terminal still gets the staged CLI in a custom non-root
// image, while the disabled switch gives it no socket or coordination bind.
func TestIntegrationCoordinationCLIWhenDisabled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("the disabled CLI scenario needs a reachable Docker daemon")
	}
	image, user := buildCoordAgentImage(t)
	docker, _, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("the Docker daemon went away after the image was built")
	}
	e := &coordEnv{
		rt: docker, image: image, serverBinary: buildServerBinary(t),
		dataDir: filepath.Join(shortTempDir(t), "data"),
	}
	srv := e.seed(ctx, t, true)
	ctrl, memberClient := srv.control(t, e.ada.key)
	run := e.launch(t, ctrl, "", "pi")
	att := openAttach(t, memberClient, run.ID)
	if _, err := att.stdin.Write([]byte("aether-cli-start\r")); err != nil {
		t.Fatalf("start disabled shell fixture: %v", err)
	}
	att.waitOutput(t, "cli-no-auto-mcp:")
	att.waitOutput(t, "cli-help:")
	att.waitOutput(t, "cli-status-no-socket:")
	att.waitOutput(t, "cli-digest:"+fileDigest(t, e.serverBinary))
	att.waitOutput(t, "cli-disabled-no-socket:")
	assertNoAgentError(t, att)
	e.assertDisabledCLIOnly(ctx, t, newDockerCLI(t), run.ID, user)
}

// TestIntegrationCoordinationMissionIntegratorInContainer proves a mission's
// integrator container learns its role from the staged CLI alone: status
// carries the live assignment and skill leads with the integrator role
// before any phase guidance.
func TestIntegrationCoordinationMissionIntegratorInContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("the mission integrator scenario needs a reachable Docker daemon")
	}
	// The fake harness stays idle so the test drives the CLI itself.
	t.Setenv("AETHER_FAKE_AGENT", "sleep 300")
	image, _ := buildCoordAgentImage(t)
	docker, _, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("the Docker daemon went away after the image was built")
	}
	e := &coordEnv{
		rt: docker, image: image, serverBinary: buildServerBinary(t),
		dataDir: filepath.Join(shortTempDir(t), "data"),
	}
	srv := e.seed(ctx, t, false)
	ctrl, _ := srv.control(t, e.ada.key)

	integrator := protocol.MissionIntegrator{
		AccountMemberID: string(e.ada.id), Harness: "fake", Mode: string(domain.LaunchTUI),
	}
	var created protocol.MissionCreateResult
	if err := ctrl.Call(protocol.MethodMissionCreate, protocol.MissionCreateParams{
		WorkspaceID: string(e.ws.ID), Objective: "container integrator fixture",
		AccountableHumanID: string(e.ada.id), Integrator: integrator,
		ExecutionChoices: []protocol.MissionExecutionChoice{{
			AccountMemberID: integrator.AccountMemberID, Harness: integrator.Harness, Mode: integrator.Mode,
		}},
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 1, IdempotencyKey: "container-integrator",
	}, &created); err != nil {
		t.Fatalf("mission.create: %v", err)
	}
	missionID, run := created.Mission.ID, created.Mission.CurrentIntegratorRunID
	if missionID == "" || run == "" {
		t.Fatalf("mission.create returned incomplete mission: %+v", created.Mission)
	}
	waitMissionSocket(t, e.coordDir(run))

	internalCLI := func(args ...string) string {
		t.Helper()
		argv := append([]string{"exec", containerName(run), coordtransport.CLIPath}, args...)
		out, err := exec.CommandContext(ctx, "docker", argv...).CombinedOutput()
		if err != nil {
			t.Fatalf("aether-internal %s in run %s: %v (%s)", strings.Join(args, " "), run, err, out)
		}
		return string(out)
	}

	statusOut := internalCLI("status")
	var envelope missionCLIEnvelope
	if err := json.Unmarshal([]byte(statusOut), &envelope); err != nil || !envelope.OK {
		t.Fatalf("aether-internal status = %q (decode error %v)", statusOut, err)
	}
	var status protocol.CoordStatusResult
	if err := json.Unmarshal(envelope.Result, &status); err != nil {
		t.Fatalf("decode status result %s: %v", envelope.Result, err)
	}
	if a := status.Assignment; a == nil || a.Role != "integrator" || a.MissionID != missionID ||
		a.Phase != string(domain.MissionPhasePlanning) {
		t.Fatalf("integrator status assignment = %+v, want role integrator, mission %s, phase planning", a, missionID)
	}

	skill := internalCLI("skill")
	role := strings.Index(skill, "You are this mission's integrator")
	phase := strings.Index(skill, "Phase: ")
	if role < 0 || phase < 0 || role > phase {
		t.Fatalf("aether-internal skill must print the integrator role before the phase:\n%s", skill)
	}
}

// buildCoordAgentImage builds the run image this scenario launches:
// busybox, the fixture agent installed as the "claude" executable the
// shipped profile launches, two shell-capable harness shims, and a non-root
// user.
//
// That user is this process's own wherever it is not root, because the
// scheduler's ownership pass chowns the run checkout and the member home
// to the container user before the container is created, and an unprivileged
// test process can only chown to itself.
func buildCoordAgentImage(t *testing.T) (image, user string) {
	t.Helper()
	dir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "claude"), "./internal/server/testdata/coordagent")
	build.Dir = repoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build the in-container agent: %v (%s)", buildErr, out)
	}
	writeFile(t, filepath.Join(dir, "shell-coord-agent"), `#!/bin/sh
set -eu
task=${1:-shell coordination}

fail() {
	echo "cli-fail:$*" >&2
	exit 1
}

# The test releases the fixture only after the attach subsystem acknowledges
# its stream, so every marker below is observable rather than a startup race.
if ! IFS= read -r start; then
	fail "start handshake"
fi
[ "$start" = "aether-cli-start" ] || fail "start handshake"
printf '%s:initial\n' "$AETHER_RUN_ID" > shared.txt
for arg in "$@"; do
	[ "$arg" = "--mcp-config" ] && fail "automatic MCP config"
done
echo "cli-no-auto-mcp:$AETHER_RUN_ID"

help=$(/usr/local/bin/aether-internal --help) || fail help
case "$help" in
	*"aether-internal"*) echo "cli-help:$AETHER_RUN_ID" ;;
	*) fail "help output missing command name" ;;
esac
skill=$(/usr/local/bin/aether-internal skill) || fail skill
case "$skill" in
	?*) echo "cli-skill-available:$AETHER_RUN_ID" ;;
	*) fail "skill output was empty" ;;
esac
# The CLI and bridge are the same staged binary when coordination is live,
# but the CLI remains intentionally present when the bridge is disabled.
digest=$(sha256sum /usr/local/bin/aether-internal |
	awk 'NR == 1 {print $1}')
[ -n "$digest" ] || fail "digest unavailable"
echo "cli-digest:$digest"

# The disabled server still gives every image the version-matched CLI, but it
# deliberately gives no run socket or coordination directory.
if [ ! -e /run/aether/coord3.sock ]; then
	status_code=0
	status=$(/usr/local/bin/aether-internal status) || status_code=$?
	[ "$status_code" -eq 4 ] || fail "no-socket status exit:$status_code:$status"
	case "$status" in
		*'"ok":false'*'"code":-32004'*) echo "cli-status-no-socket:$AETHER_RUN_ID" ;;
		*) fail "no-socket status:$status" ;;
	esac
	echo "cli-disabled-no-socket:$AETHER_RUN_ID"
	sleep 60
	exit 0
fi
case "$skill" in
	*"Run:"*) echo "cli-skill-live:$AETHER_RUN_ID" ;;
	*) fail "skill did not report live run identity" ;;
esac

# The diff watcher may register after the first edit. Re-touch with changing
# content every 10 seconds, with quiet polling between writes, for the same
# two-minute window as the proven coordination fixture.
sleep 3

peer=
last_status=
attempt=0
while [ "$attempt" -lt 120 ]; do
	if [ "$attempt" -gt 0 ] && [ $((attempt % 10)) -eq 0 ]; then
		printf '%s:retouch-%s\n' "$AETHER_RUN_ID" "$attempt" > shared.txt
	fi
	status=$(/usr/local/bin/aether-internal status --json) || {
		code=$?
		echo "cli-fail:status:$code:$status" >&2
		exit "$code"
	}
	last_status=$status
	peer=$(printf '%s\n' "$status" | sed -nE 's/.*"peers":\[\{"run_id":"([^"]*)".*/\1/p')
	if [ -n "$peer" ]; then
		break
	fi
	attempt=$((attempt + 1))
	sleep 1
done
if [ -z "$peer" ]; then
	fail "no peer status:$last_status"
fi

/usr/local/bin/aether-internal send \
	--to "$peer" \
	--body "$task" \
	--idempotency-key "shell-send-$AETHER_RUN_ID" >/dev/null || fail send
echo "cli-status:$AETHER_RUN_ID"
echo "cli-sent:$AETHER_RUN_ID:$peer"

attempt=0
while [ "$attempt" -lt 20 ]; do
	inbox=$(/usr/local/bin/aether-internal inbox --wait 2)
	ack=$(printf '%s\n' "$inbox" | sed -n 's/.*"ack_token":"\([^"]*\)".*/\1/p')
	if [ -n "$ack" ]; then
		/usr/local/bin/aether-internal inbox --ack "$ack" >/dev/null || fail ack
		echo "cli-inbox:$AETHER_RUN_ID"
		echo "cli-acked:$AETHER_RUN_ID"
		break
	fi
	attempt=$((attempt + 1))
done
[ -n "$ack" ] || fail "no message"
report=$(/usr/local/bin/aether-internal report \
	--outcome success \
	--summary "shell coordination completed" \
	--idempotency-key "shell-report-$AETHER_RUN_ID") || fail report
case "$report" in
	*'"ok":true'*) echo "cli-reported:$AETHER_RUN_ID" ;;
	*) fail "report not acknowledged:$report" ;;
esac
`)
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 1000, 1000
	}
	user = fmt.Sprintf("%d:%d", uid, gid)
	writeFile(t, filepath.Join(dir, "Dockerfile"),
		"FROM busybox\n"+
			"COPY claude /usr/local/bin/claude\n"+
			"COPY shell-coord-agent /usr/local/bin/pi\n"+
			"COPY shell-coord-agent /usr/local/bin/omp\n"+
			"RUN chmod +x /usr/local/bin/pi /usr/local/bin/omp\n"+
			"USER "+user+"\n")
	image = fmt.Sprintf("aether-e2e-coordagent:%d", os.Getpid())
	if out, err := exec.Command("docker", "build", "-q", "-t", image, dir).CombinedOutput(); err != nil {
		t.Fatalf("docker build %s: %v (%s)", image, err, out)
	}
	t.Cleanup(func() {
		out, err := exec.Command("docker", "rmi", "-f", image).CombinedOutput()
		if err != nil {
			t.Logf("remove image %s: %v (%s)", image, err, out)
		}
	})
	return image, user
}

// fileDigest is the content hash staged binaries are named by.
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// resolved is the path the daemon reports for a bind: the server passes
// mount sources through EvalSymlinks before handing them to the runtime.
func resolved(t *testing.T, path string) string {
	t.Helper()
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return target
}
