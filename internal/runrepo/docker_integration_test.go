//go:build integration

package runrepo

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// A server without root cannot chown the checkout, so a root container user
// meets a repository another uid owns. A root test process stands in for that
// owner with nobody's uid.
func TestIntegrationRootContainerTrustsUnprivilegedCheckout(t *testing.T) {
	checkout := t.TempDir()
	gitCmd(t, checkout, "init", "-b", "main")
	gitCmd(t, checkout, "-c", "user.name=Run Author", "-c", "user.email=run@example.test", "commit", "--allow-empty", "-m", "initial")
	if os.Geteuid() == 0 {
		if err := filepath.WalkDir(checkout, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(path, 65534, 65534)
		}); err != nil {
			t.Fatalf("hand the checkout to nobody: %v", err)
		}
	}

	image := fmt.Sprintf("aether-runrepo-git:%d", os.Getpid())
	build := exec.Command("docker", "build", "-q", "-t", image, "-")
	build.Stdin = strings.NewReader("FROM alpine:3.21\nRUN apk add --no-cache git\n")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build %s: %v (%s)", image, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

	d, err := runtime.NewDocker(runtime.WithLabels(map[string]string{"aether.test": t.Name()}), runtime.WithNetworkMode("none"))
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Create(t.Context(), runtime.Spec{
		Image:             image,
		Command:           []string{"sleep", "300"},
		WorktreeHostPath:  checkout,
		WorktreeMountPath: "/workspace",
		WorkingDir:        "/workspace",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.Destroy(ctx, id); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	})
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start: %v", err)
	}

	code, _, stderr, err := d.Exec(t.Context(), id, []string{"env", "GIT_CONFIG_COUNT=0", "git", "-C", "/workspace", "status"}, "")
	if err != nil || code == 0 || !strings.Contains(stderr, "dubious ownership") {
		t.Fatalf("untrusted git status: code=%d err=%v stderr=%q, want the dubious ownership refusal", code, err, stderr)
	}
	code, _, stderr, err = d.Exec(t.Context(), id, []string{"git", "-C", "/workspace", "status"}, "")
	if err != nil || code != 0 {
		t.Fatalf("git status as root: code=%d err=%v stderr=%q", code, err, stderr)
	}

	status, err := New(d.Exec).Status(t.Context(), Execution{
		ContainerID: id,
		WorkDir:     "/workspace",
		Authorize:   func(context.Context, bool) error { return nil },
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Branch != "main" || status.Head == "" {
		t.Fatalf("Status = %+v, want branch main with a head", status)
	}
}
