//go:build integration

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/3xDevOps/Aether/internal/coordcli"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
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
	// was launched with, the user it runs as, both binds read-only in the
	// kernel's own mount table, the coordination directory it traverses
	// with the config and socket modes it finds there, that directory
	// refusing a write with EROFS, and the staged binary it executes as the
	// bridge.
	for _, att := range []*attachConn{attA, attB} {
		att.waitOutput(t, "--mcp-config "+mcpConfigTarget)
		att.waitOutput(t, "user:"+user)
		att.waitOutput(t, "mode:"+mcpbridge.MountDir+"=0755")
		att.waitOutput(t, coord.ConfigName+"=0444")
		att.waitOutput(t, coord.CoAuthorsName+"=0444")
		att.waitOutput(t, coord.SocketName+"=0666")
		att.waitOutput(t, "mount:"+mcpbridge.MountDir+"=ro")
		att.waitOutput(t, "readonly:"+mcpbridge.MountDir)
		att.waitOutput(t, "mode:"+mcpbridge.BinaryPath+"=0555")
		att.waitOutput(t, "mount:"+mcpbridge.BinaryPath+"=ro")
		att.waitOutput(t, "bridge:"+mcpbridge.BinaryPath+" mcp")
	}

	// The round trip: each agent found its peer through aether_status,
	// messaged it with aether_send, and read the peer's message out of
	// aether_inbox - every call served by the staged binary inside its own
	// container, over the socket its own mount carries.
	attA.waitOutput(t, "inbox:handled by "+runB.ID)
	attB.waitOutput(t, "inbox:handled by "+runA.ID)
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
	runB := e.launch(t, boCtrl, "shell coordination B", "omp")
	attA := openAttach(t, adaClient, runA.ID)
	cli := newDockerCLI(t)
	attB := openAttach(t, boClient, runB.ID)
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
	for _, att := range []*attachConn{attA, attB} {
		att.waitOutput(t, "cli-status:")
		att.waitOutput(t, "cli-sent:")
		att.waitOutput(t, "cli-inbox:")
		att.waitOutput(t, "cli-acked:")
		assertNoAgentError(t, att)
	}
}

// assertRealizedMounts reads the two coordination binds back off the live
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
		mcpbridge.BinaryPath: resolved(t, staged),
		coordcli.BinaryPath:  resolved(t, staged),
		mcpbridge.MountDir:   resolved(t, e.coordDir(run)),
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
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the in-container agent: %v (%s)", err, out)
	}
	writeFile(t, filepath.Join(dir, "shell-coord-agent"), `#!/bin/sh
set -eu
task=${1:-shell coordination}

printf '%s:initial\n' "$AETHER_RUN_ID" > shared.txt
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
	peer=$(printf '%s\n' "$status" | awk -F '"run_id":"' 'NF > 2 { split($3, p, "\""); print p[1]; exit }')
	if [ -n "$peer" ]; then
		break
	fi
	attempt=$((attempt + 1))
	sleep 1
done
if [ -z "$peer" ]; then
	echo "cli-fail:no peer status:$last_status" >&2
	exit 1
fi

/usr/local/bin/aether-internal send \
	--to "$peer" \
	--body "$task" \
	--idempotency-key "shell-send-$AETHER_RUN_ID" >/dev/null
echo "cli-status:$AETHER_RUN_ID"
echo "cli-sent:$AETHER_RUN_ID:$peer"

attempt=0
while [ "$attempt" -lt 20 ]; do
	inbox=$(/usr/local/bin/aether-internal inbox --wait 2)
	ack=$(printf '%s\n' "$inbox" | sed -n 's/.*"ack_token":"\([^"]*\)".*/\1/p')
	if [ -n "$ack" ]; then
		/usr/local/bin/aether-internal inbox --ack "$ack" >/dev/null
		echo "cli-inbox:$AETHER_RUN_ID"
		echo "cli-acked:$AETHER_RUN_ID"
		exit 0
	fi
	attempt=$((attempt + 1))
done
echo "cli-fail:no message" >&2
exit 1
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
