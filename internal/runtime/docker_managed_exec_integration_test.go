//go:build integration && linux

package runtime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/moby/moby/client"
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
				Argv: []string{"/bin/sh", "-c", `trap '' HUP TERM; sleep 300 & echo $! > /workspace/owned.pid; echo owned-ready; read answer; echo input:$answer; wait`},
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

// TestDockerManagedExecForgottenByDocker: dockerd restarted with
// live-restore forgets every exec, while the processes keep running. A
// forgotten exec is recovered, stopped and read from its helper; only a
// stopped container or a helper with no supervisor proves it gone.
func TestDockerManagedExecForgottenByDocker(t *testing.T) {
	d := newTestDocker(t)
	binary := buildStagedHelper(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	id := createContainer(t, d, Spec{
		Name:  fmt.Sprintf("it-forgotten-exec-%d", time.Now().UnixNano()),
		Image: testImage, TTY: true,
		Command: []string{"/bin/sh", "-c", "exec sleep 300"},
		Mounts:  []Mount{{HostPath: binary, ContainerPath: coordtransport.CLIPath, ReadOnly: true}},
	})
	if err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	var forgottenIDs sync.Map
	cli, err := client.New(client.FromEnv, client.WithResponseHook(func(resp *http.Response) {
		path := resp.Request.URL.Path
		if _, forget := forgottenIDs.Load(filepath.Base(filepath.Dir(path))); forget && resp.Request.Method == http.MethodGet && strings.HasSuffix(path, "/json") {
			_ = resp.Body.Close()
			resp.StatusCode, resp.Status = http.StatusNotFound, "404 Not Found"
			resp.Header.Set("Content-Type", "application/json")
			resp.Body = io.NopCloser(strings.NewReader(`{"message":"No such exec instance"}`))
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	forgetful := &Docker{cli: cli, waitClient: cli, namePrefix: d.namePrefix, labels: d.labels, networkMode: d.networkMode}
	start := func(key string) ExecIdentity {
		t.Helper()
		execution, err := d.StartExecPipe(ctx, id, ExecSpec{CreationKey: key, Argv: []string{"sleep", "300"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := execution.Detach(); err != nil {
			t.Fatal(err)
		}
		forgottenIDs.Store(execution.Identity().ExecID, true)
		return execution.Identity()
	}

	recovered, err := forgetful.RecoverExec(ctx, start("live"))
	if err != nil {
		t.Fatalf("recover a forgotten exec whose command runs: %v", err)
	}
	state, err := recovered.Status(ctx)
	if err != nil || !state.Running || state.Exited {
		t.Fatalf("forgotten running exec = %+v, %v", state, err)
	}
	status, err := recovered.Stop(ctx, time.Second)
	if err != nil || status.Code != 143 {
		t.Fatalf("stop a forgotten exec = %+v, %v; want SIGTERM exit 143", status, err)
	}
	if state, err = recovered.Status(ctx); err != nil || !state.Exited || state.ExitCode == nil || *state.ExitCode != 143 {
		t.Fatalf("forgotten exec after stop = %+v, %v", state, err)
	}

	orphan := start("orphan")
	if code, _, stderr, err := d.Exec(ctx, id, []string{"pkill", "-KILL", "-f", "run-pipe orphan"}, ""); err != nil || code != 0 {
		t.Fatalf("kill the supervisor: code=%d stderr=%q err=%v", code, stderr, err)
	}
	if _, err := forgetful.RecoverExec(ctx, orphan); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recover a forgotten exec without a supervisor = %v; want ErrNotFound", err)
	}

	stopped := start("stopped")
	if err := d.Stop(ctx, id, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := forgetful.RecoverExec(ctx, stopped); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recover a forgotten exec in a stopped container = %v; want ErrNotFound", err)
	}
}

// TestDockerManagedExecHelperLost: Docker still records an exec whose helper
// died and took its state with it. The command is gone and its exit status
// lost: recovery, status and stop all say so at once instead of failing as
// unavailable forever, and a live exec beside it keeps running.
func TestDockerManagedExecHelperLost(t *testing.T) {
	d := newTestDocker(t)
	binary := buildStagedHelper(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	id := createContainer(t, d, Spec{
		Name:  fmt.Sprintf("it-lost-exec-%d", time.Now().UnixNano()),
		Image: testImage, TTY: true,
		Command: []string{"/bin/sh", "-c", "exec sleep 300"},
		Mounts:  []Mount{{HostPath: binary, ContainerPath: coordtransport.CLIPath, ReadOnly: true}},
	})
	if err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	start := func(key string) ManagedExec {
		t.Helper()
		execution, err := d.StartExecPipe(ctx, id, ExecSpec{CreationKey: key, Argv: []string{"sleep", "300"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := execution.Detach(); err != nil {
			t.Fatal(err)
		}
		return execution
	}
	live, lost := start("live"), start("lost")
	sum := sha256.Sum256([]byte("lost"))
	stateDir := "/tmp/.aether-devexec-" + hex.EncodeToString(sum[:20])
	for _, argv := range [][]string{{"pkill", "-KILL", "-f", "run-pipe lost"}, {"rm", "-rf", stateDir}} {
		if code, _, stderr, err := d.Exec(ctx, id, argv, ""); err != nil || code != 0 {
			t.Fatalf("%v: code=%d stderr=%q err=%v", argv, code, stderr, err)
		}
	}
	for {
		info, err := d.cli.ExecInspect(ctx, lost.Identity().ExecID, client.ExecInspectOptions{})
		if err != nil {
			t.Fatalf("Docker forgot the exec: %v", err)
		}
		if !info.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := d.RecoverExec(ctx, lost.Identity()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recover an exec whose helper left no record = %v; want ErrNotFound", err)
	}
	if state, err := lost.Status(ctx); !errors.Is(err, ErrNotFound) || state.Exited {
		t.Fatalf("status of an exec whose helper left no record = %+v, %v; want ErrNotFound and no exit status", state, err)
	}
	began := time.Now()
	if status, err := lost.Stop(ctx, 0); !errors.Is(err, ErrNotFound) || time.Since(began) > 5*time.Second {
		t.Fatalf("stop an exec whose helper left no record = %+v, %v after %s; want ErrNotFound at once", status, err, time.Since(began))
	}

	recovered, err := d.RecoverExec(ctx, live.Identity())
	if err != nil {
		t.Fatalf("recover the live exec: %v", err)
	}
	if state, err := recovered.Status(ctx); err != nil || !state.Running {
		t.Fatalf("live exec beside a lost one = %+v, %v", state, err)
	}
}
