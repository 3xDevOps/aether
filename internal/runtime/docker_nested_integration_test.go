//go:build integration

package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/moby/moby/client"
)

// The status file is renamed into place last, so its existence means the
// version output and the optional /m write are complete.
const nestedGateProbe = `docker version >/tmp/v.tmp 2>&1; s=$?; [ ! -d /m ] || touch /m/main-wrote; mv /tmp/v.tmp /tmp/main-docker-version; echo $s >/tmp/s.tmp; mv /tmp/s.tmp /tmp/main-docker-status; exec sleep 3600`

func startNestedDocker(t *testing.T, ctx context.Context, docker *runtime.Docker, image, user string, mounts ...runtime.Mount) runtime.ID {
	t.Helper()
	id, err := docker.Create(ctx, runtime.Spec{
		Image: image, User: user, NestedDocker: true, Mounts: mounts,
		CPULimit: 2, MemoryLimitBytes: 2 << 30,
		Command: []string{"/bin/sh", "-c", nestedGateProbe},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Destroy(context.Background(), id) })
	if err := docker.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func execOK(t *testing.T, ctx context.Context, docker *runtime.Docker, id runtime.ID, script string) string {
	t.Helper()
	code, stdout, stderr, err := docker.Exec(ctx, id, []string{"/bin/sh", "-ec", script}, "/tmp")
	if err != nil || code != 0 {
		t.Fatalf("exec %q: exit %d, err %v\nstdout: %s\nstderr: %s", script, code, err, stdout, stderr)
	}
	return stdout
}

// mainDockerStatus waits for the gated main process to publish the exit code
// of its `docker version`.
func mainDockerStatus(t *testing.T, ctx context.Context, docker *runtime.Docker, id runtime.ID) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, stdout, _, err := docker.Exec(ctx, id, []string{"cat", "/tmp/main-docker-status"}, "/tmp")
		if err == nil && code == 0 {
			return strings.TrimSpace(stdout)
		}
		if time.Now().After(deadline) {
			t.Fatalf("main process did not publish its docker version status: exit %d, err %v", code, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Needs a sysbox host and an image holding dockerd, the docker CLI, compose
// and buildx, e.g. the standard image built from images/standard.
func TestDockerNestedDocker(t *testing.T) {
	image := os.Getenv("AETHER_NESTED_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("AETHER_NESTED_DOCKER_TEST_IMAGE is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	docker, err := runtime.NewDocker(runtime.WithContainerRuntime(runtime.RuntimeAuto))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	id := startNestedDocker(t, ctx, docker, image, "0:0")

	sdk, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer sdk.Close()
	inspected, err := sdk.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := inspected.Container.HostConfig.Runtime; got != "sysbox-runc" {
		t.Fatalf("HostConfig.Runtime = %q, want sysbox-runc", got)
	}
	if inspected.Container.HostConfig.Privileged {
		t.Fatal("nested Docker container is privileged")
	}

	if status := mainDockerStatus(t, ctx, docker, id); status != "0" {
		t.Fatalf("main process ran before dockerd was ready: docker version exit %s\n%s", status, execOK(t, ctx, docker, id, "cat /tmp/main-docker-version"))
	}
	if err := docker.Start(ctx, id); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if count := strings.TrimSpace(execOK(t, ctx, docker, id, "pgrep -x dockerd | wc -l")); count != "1" {
		t.Fatalf("dockerd processes after second Start = %s, want 1", count)
	}
	if err := docker.Pause(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := docker.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	execOK(t, ctx, docker, id, "docker version")

	execOK(t, ctx, docker, id, "docker run --rm busybox:1.36 true")
	execOK(t, ctx, docker, id, `mkdir -p compose && cat > compose/compose.yaml <<'YAML'
services:
  web:
    image: busybox:1.36
    command: ["sh", "-c", "echo nested-ok > /index.html && httpd -f -p 8080 -h /"]
    ports: ["18080:8080"]
YAML`)
	execOK(t, ctx, docker, id, "docker compose -f compose/compose.yaml up -d --wait")
	defer func() {
		_, _, _, _ = docker.Exec(context.Background(), id, []string{"docker", "compose", "-f", "/tmp/compose/compose.yaml", "down", "-v"}, "/tmp")
	}()
	body := execOK(t, ctx, docker, id, `for i in $(seq 30); do wget -qO- http://127.0.0.1:18080/index.html && exit 0; sleep 1; done; exit 1`)
	if strings.TrimSpace(body) != "nested-ok" {
		t.Fatalf("published port body = %q, want nested-ok", body)
	}
	execOK(t, ctx, docker, id, "docker compose -f compose/compose.yaml down -v")
	for _, user := range []string{"12345:12345", "1001:0"} {
		t.Run("non-root user "+user, func(t *testing.T) {
			nested := startNestedDocker(t, ctx, docker, image, user)
			if status := mainDockerStatus(t, ctx, docker, nested); status != "0" {
				t.Fatalf("docker version as %s exit %s\n%s", user, status, execOK(t, ctx, docker, nested, "cat /tmp/main-docker-version"))
			}
		})
	}
	t.Run("ID-mapped bind mount", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chown(dir, 1000, 1000); err != nil {
			t.Fatal(err)
		}
		nested := startNestedDocker(t, ctx, docker, image, "1000:1000", runtime.Mount{HostPath: dir, ContainerPath: "/m"})
		mainDockerStatus(t, ctx, docker, nested)
		info, err := os.Stat(filepath.Join(dir, "main-wrote"))
		if err != nil {
			t.Fatalf("main process could not write the bind mount: %v", err)
		}
		if uid := info.Sys().(*syscall.Stat_t).Uid; uid != 1000 {
			t.Fatalf("file written through the bind mount is owned by uid %d on the host, want 1000", uid)
		}
	})
	t.Run("saved environment", func(t *testing.T) {
		tag := "aether-nested-test:saved"
		execOK(t, ctx, docker, id, "rm -f /tmp/main-docker-status /tmp/main-docker-version")
		if err := docker.Commit(ctx, id, tag); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = docker.RemoveImage(context.Background(), tag) })
		saved := startNestedDocker(t, ctx, docker, tag, "0:0")
		if status := mainDockerStatus(t, ctx, docker, saved); status != "0" {
			t.Fatalf("dockerd did not start from the committed image: docker version exit %s\n%s", status, execOK(t, ctx, docker, saved, "cat /var/log/aether-dockerd.log"))
		}
		execOK(t, ctx, docker, saved, "docker run --rm busybox:1.36 true")

		plain, err := docker.Create(ctx, runtime.Spec{Image: tag, User: "0:0", Command: []string{"sleep", "3600"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = docker.Destroy(context.Background(), plain) })
		if err := docker.Start(ctx, plain); err != nil {
			t.Fatalf("a non-nested container from the saved image must start without dockerd: %v", err)
		}
		if count := strings.TrimSpace(execOK(t, ctx, docker, plain, "pgrep -x dockerd | wc -l")); count != "0" {
			t.Fatalf("dockerd processes in a non-nested container = %s, want 0", count)
		}
	})
	execOK(t, ctx, docker, id, `mkdir -p build && printf 'FROM busybox:1.36\nRUN echo built > /built\n' > build/Dockerfile && docker buildx build --load -t nested-built:test build && docker run --rm nested-built:test cat /built`)
}

// Build the browser image first as TestDockerBrowserCompanion documents.
func TestDockerBrowserJoinsNestedDockerRun(t *testing.T) {
	image := os.Getenv("AETHER_NESTED_DOCKER_TEST_IMAGE")
	browserImage := os.Getenv("AETHER_BROWSER_TEST_IMAGE")
	if image == "" || browserImage == "" {
		t.Skip("AETHER_NESTED_DOCKER_TEST_IMAGE and AETHER_BROWSER_TEST_IMAGE are required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	docker, err := runtime.NewDocker(runtime.WithContainerRuntime(runtime.RuntimeAuto))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	app := startNestedDocker(t, ctx, docker, image, "0:0")
	root, err := os.MkdirTemp("", "browser-nested-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manager, err := browser.NewManager(root, docker, browser.Config{Image: browserImage, CPULimit: 1, MemoryLimitBytes: 2 << 30})
	if err != nil {
		t.Fatal(err)
	}
	run := browser.Run{ID: "nested-docker-run", ContainerID: app}
	t.Cleanup(func() { _ = manager.Remove(context.Background(), run) })
	status, control, err := manager.Ensure(ctx, run)
	if err != nil {
		t.Fatalf("browser companion joining a sysbox run container: %v", err)
	}
	defer control.Close()
	info, err := docker.InspectBrowser(ctx, status.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if info.RunContainer != app || info.State != "running" {
		t.Fatalf("companion inspect: %+v", info)
	}
}
