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
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/3xDevOps/Aether/internal/coord"
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
	waitEvent(t, sub, &seen, "run A's coordination note", coordNote(runA.ID, e.ada.id, runB.ID))
	waitEvent(t, sub, &seen, "run B's coordination note", coordNote(runB.ID, e.bo.id, runA.ID))
	for _, att := range []*attachConn{attA, attB} {
		assertNoAgentError(t, att)
	}
}

// assertRealizedMounts reads the two coordination binds back off the live
// container. The bridge source must be the staged copy of the very binary
// the server was told to stage, named by its own content hash.
func (e *coordEnv) assertRealizedMounts(ctx context.Context, t *testing.T, cli *client.Client, run, user string) {
	t.Helper()
	insp, err := cli.ContainerInspect(ctx, containerName(run))
	if err != nil {
		t.Fatalf("inspect run %s's container: %v", run, err)
	}
	if insp.Config.User != user {
		t.Errorf("run %s container user = %q, want the image's non-root %q", run, insp.Config.User, user)
	}
	staged := filepath.Join(e.dataDir, "runtime", "bin", "aether-server-"+fileDigest(t, e.serverBinary))
	want := map[string]string{
		mcpbridge.BinaryPath: resolved(t, staged),
		mcpbridge.MountDir:   resolved(t, e.coordDir(run)),
	}
	realized := make(map[string]container.MountPoint, len(insp.Mounts))
	for _, m := range insp.Mounts {
		realized[m.Destination] = m
	}
	for target, source := range want {
		m, ok := realized[target]
		switch {
		case !ok:
			t.Errorf("run %s has no realized mount at %s: %+v", run, target, insp.Mounts)
		case m.Source != source:
			t.Errorf("run %s mount %s comes from %q, want %q", run, target, m.Source, source)
		case m.RW:
			t.Errorf("run %s mount %s is writable", run, target)
		}
	}
}

// buildCoordAgentImage builds the run image this scenario launches:
// busybox, the fixture agent installed as the "claude" executable the
// shipped profile launches, and a non-root user.
//
// That user is this process's own wherever it is not root, because the
// scheduler's ownership pass chowns the run checkout and the member home to
// the container user before the container is created, and an unprivileged
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
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 1000, 1000
	}
	user = fmt.Sprintf("%d:%d", uid, gid)
	writeFile(t, filepath.Join(dir, "Dockerfile"),
		"FROM busybox\nCOPY claude /usr/local/bin/claude\nUSER "+user+"\n")
	image = fmt.Sprintf("aether-e2e-coordagent:%d", os.Getpid())
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
