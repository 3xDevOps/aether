//go:build integration && linux

package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
)

// buildStagedHelper builds the server binary the container runs as
// aether-internal dev-exec.
func buildStagedHelper(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "aether-server")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/aether-server")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build staged helper: %v (%s)", err, output)
	}
	return binary
}

func TestDockerManagedExecLifecycle(t *testing.T) {
	d := newTestDocker(t)
	binary := buildStagedHelper(t)
	for _, user := range []string{"0:0", "65534:65534"} {
		t.Run(user, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			worktree := t.TempDir()
			if err := os.Chmod(worktree, 0o777); err != nil {
				t.Fatal(err)
			}
			id := createContainer(t, d, Spec{
				Name:  fmt.Sprintf("it-owned-exec-%d", time.Now().UnixNano()),
				Image: testImage, User: user, TTY: true,
				WorktreeHostPath: worktree, WorktreeMountPath: "/workspace", WorkingDir: "/workspace",
				Command: []string{"/bin/sh", "-c", "exec sleep 300"},
				Env:     map[string]string{"OWNED_TEST_ENV": "inherited"},
				Mounts:  []Mount{{HostPath: binary, ContainerPath: coordtransport.CLIPath, ReadOnly: true}},
			})
			if err := d.Start(ctx, id); err != nil {
				t.Fatal(err)
			}
			fast, err := d.StartExecTTY(ctx, id, ExecSpec{
				CreationKey: "fast", Cols: 80, Rows: 24,
				Argv: []string{"/bin/sh", "-c", `printf 'identity:%s:%s:%s\n' "$(id -u)" "$PWD" "$OWNED_TEST_ENV"; exit 7`},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer fast.Detach()
			att := fast.Attachment()
			if att == nil {
				t.Fatal("fast command lost its buffered output before attachment")
			}
			status, err := fast.Wait(ctx)
			if err != nil || status.Code != 7 {
				t.Fatalf("fast command exit = %+v, %v", status, err)
			}
			output, err := io.ReadAll(att.Stdout())
			want := "identity:" + strings.Split(user, ":")[0] + ":/workspace:inherited"
			if err != nil || !strings.Contains(string(output), want) {
				t.Fatalf("configured execution identity/output = %q, %v; want %q", output, err, want)
			}

			spec := ExecSpec{
				CreationKey: "long-running", Cols: 80, Rows: 24,
				Argv: []string{"/bin/sh", "-c", `trap '' TERM; sleep 300 & echo $! > /workspace/owned.pid; echo owned-ready; read answer; echo input:$answer; wait`},
			}
			owned, err := d.StartExecTTY(ctx, id, spec)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.Detach()
			lines := readLines(owned.Attachment())
			waitLine(t, lines, 10*time.Second, "owned-ready")
			if _, err := owned.Attachment().Stdin().Write([]byte("accepted\n")); err != nil {
				t.Fatal(err)
			}
			waitLine(t, lines, 10*time.Second, "input:accepted")
			if _, err := d.StartExecTTY(ctx, id, spec); err == nil {
				t.Fatal("duplicate creation key silently reused or reran the command")
			}
			if err := owned.Detach(); err != nil {
				t.Fatal(err)
			}
			recovered, err := d.RecoverExec(ctx, owned.Identity())
			if err != nil {
				t.Fatal(err)
			}
			state, err := recovered.Status(ctx)
			if err != nil || !state.Running || state.Attached || state.UnavailableReason == "" || recovered.Attachment() != nil {
				t.Fatalf("detach/recovery invented a PTY or stopped the command: %+v, %v", state, err)
			}
			status, err = recovered.Stop(ctx, 20*time.Millisecond)
			if err != nil || status.Code != 137 {
				t.Fatalf("explicit stop = %+v, %v", status, err)
			}
			code, stdout, stderr, err := d.Exec(ctx, id, []string{"/bin/sh", "-c", `pid=$(cat /workspace/owned.pid); test ! -e /proc/$pid && printf run-alive`}, "")
			if err != nil || code != 0 || stdout != "run-alive" {
				t.Fatalf("stop killed the run or left an owned descendant: code=%d stdout=%q stderr=%q err=%v", code, stdout, stderr, err)
			}
			if _, err := d.StartExecTTY(ctx, id, spec); err == nil {
				t.Fatal("completed creation key was reused")
			}
		})
	}
}

func TestDockerManagedExecPipe(t *testing.T) {
	d := newTestDocker(t)
	binary := buildStagedHelper(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	id := createContainer(t, d, Spec{
		Name:  fmt.Sprintf("it-pipe-exec-%d", time.Now().UnixNano()),
		Image: testImage, TTY: true,
		Command: []string{"/bin/sh", "-c", "exec sleep 300"},
		Mounts:  []Mount{{HostPath: binary, ContainerPath: coordtransport.CLIPath, ReadOnly: true}},
	})
	if err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}

	echo, err := d.StartExecPipe(ctx, id, ExecSpec{
		CreationKey: "echo",
		Argv:        []string{"/bin/sh", "-c", "test ! -t 0 && echo no-tty >&2; exec cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Detach()
	att := echo.Attachment()
	waitLine(t, readStream(att.Stderr()), 10*time.Second, "no-tty")
	lines := readLines(att)
	if _, err := att.Stdin().Write([]byte("hello pipe\n")); err != nil {
		t.Fatal(err)
	}
	waitLine(t, lines, 10*time.Second, "hello pipe")
	if err := echo.Resize(ctx, 80, 24); err == nil {
		t.Fatal("a pipe execution accepted a terminal resize")
	}
	status, err := echo.Stop(ctx, time.Second)
	if err != nil || status.Code != 143 {
		t.Fatalf("stop = %+v, %v; want SIGTERM exit 143", status, err)
	}

	// The command reads EOF once the server's stream goes away.
	eof, err := d.StartExecPipe(ctx, id, ExecSpec{CreationKey: "eof", Argv: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := eof.Detach(); err != nil {
		t.Fatal(err)
	}
	status, err = eof.Wait(ctx)
	if err != nil || status.Code != 0 {
		t.Fatalf("detached cat = %+v, %v; want exit 0 on stdin EOF", status, err)
	}

	// A command that ignores stdin outlives a server restart; the next
	// server instance proves ownership from the persisted identity and stops it.
	stale, err := d.StartExecPipe(ctx, id, ExecSpec{CreationKey: "stale", Argv: []string{"sleep", "300"}})
	if err != nil {
		t.Fatal(err)
	}
	identity := stale.Identity()
	if err := stale.Detach(); err != nil {
		t.Fatal(err)
	}
	restarted := newTestDocker(t)
	recovered, err := restarted.RecoverExec(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	state, err := recovered.Status(ctx)
	if err != nil || !state.Running || state.Attached || recovered.Attachment() != nil {
		t.Fatalf("recovered pipe execution = %+v, %v", state, err)
	}
	status, err = recovered.Stop(ctx, time.Second)
	if err != nil || status.Code != 143 {
		t.Fatalf("recovered stop = %+v, %v; want SIGTERM exit 143", status, err)
	}
	if _, err := restarted.StartExecPipe(ctx, id, ExecSpec{CreationKey: "stale", Argv: []string{"sleep", "300"}}); err == nil {
		t.Fatal("a stopped creation key was reused")
	}
}

func readStream(r io.Reader) <-chan string {
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return lines
}
