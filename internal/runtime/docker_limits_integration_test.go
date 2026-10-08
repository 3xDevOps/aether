//go:build integration

package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// The allocation exceeds only the disposable container's 64MiB ceiling, never
// the host budget. A second live container and a fresh daemon RPC prove that
// memory pressure is contained rather than taking down the worker host.
func TestDockerOOMContained(t *testing.T) {
	d := newTestDocker(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	survivor := createContainer(t, d, Spec{Image: testImage, Command: []string{"sleep", "180"}, CPULimit: 0.25, MemoryLimitBytes: 32 << 20, PidsLimit: 32})
	if err := d.Start(ctx, survivor); err != nil {
		t.Fatal(err)
	}
	oom := createContainer(t, d, Spec{
		Image:    "python:3.13-alpine",
		Command:  []string{"python3", "-c", "payload = bytearray(256 * 1024 * 1024); print(len(payload), flush=True)"},
		CPULimit: 0.5, MemoryLimitBytes: 64 << 20, PidsLimit: 32,
	})
	if err := d.Start(ctx, oom); err != nil {
		t.Fatal(err)
	}
	exit, err := d.Wait(ctx, oom)
	if err != nil {
		t.Fatal(err)
	}
	info, err := d.cli.ContainerInspect(ctx, string(oom), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if exit.Code != 137 || info.Container.State == nil || !info.Container.State.OOMKilled {
		t.Fatalf("expected cgroup OOM kill, not an allocation or setup failure: exit=%+v state=%+v", exit, info.Container.State)
	}
	if _, err := d.cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Fatalf("daemon did not survive OOM: %v", err)
	}
	code, out, stderr, err := d.Exec(ctx, survivor, []string{"sh", "-c", "printf alive"}, "")
	if err != nil || code != 0 || out != "alive" {
		t.Fatalf("other container did not survive OOM: code=%d out=%q stderr=%q err=%v", code, out, stderr, err)
	}
}

func TestDockerPIDLimitEnforced(t *testing.T) {
	d := newTestDocker(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	id := createContainer(t, d, Spec{Image: "python:3.13-alpine", Command: []string{"sleep", "120"}, CPULimit: 0.5, MemoryLimitBytes: 128 << 20, PidsLimit: 24})
	if err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	code, out, stderr, err := d.Exec(ctx, id, []string{"python3", "-c", `
import errno, os, signal, time
children = []
limited = False
try:
    for _ in range(64):
        try:
            child = os.fork()
        except OSError as error:
            if error.errno != errno.EAGAIN:
                raise
            limited = True
            break
        if child == 0:
            time.sleep(60)
            os._exit(0)
        children.append(child)
finally:
    for child in children:
        os.kill(child, signal.SIGKILL)
    for child in children:
        os.waitpid(child, 0)
if not limited or not 0 < len(children) < 24:
    raise RuntimeError("PID ceiling did not contain process creation")
print("pid-contained", end="")
`}, "")
	if err != nil || code != 0 || out != "pid-contained" {
		t.Fatalf("PID enforcement: code=%d out=%q stderr=%q err=%v", code, out, stderr, err)
	}
	code, _, stderr, err = d.Exec(ctx, id, []string{"true"}, "")
	if err != nil || code != 0 {
		t.Fatalf("PID slots not released after child cleanup: code=%d stderr=%q err=%v", code, stderr, err)
	}
}
