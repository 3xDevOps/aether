//go:build integration

package scheduler

import (
	"os"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestIntegrationLaunchAfterGoneSessionCleanupDocker reproduces the Nov 3
// state on a real engine: a merged session whose checkout and container are
// gone and whose sidecar still holds the "<unknown>" run user. Recovery
// settles it, the member's next launch starts, and a genuinely live session
// under another uid:gid still refuses a launch.
func TestIntegrationLaunchAfterGoneSessionCleanupDocker(t *testing.T) {
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = docker
		cfg.RunContainerTTL = -time.Second
		cfg.Harnesses = map[string]HarnessSpec{"fake": {TUIArgs: []string{"sh", "-c", "sleep 3600"}}}
	})
	ctx := t.Context()
	launch := func(s *Scheduler, task string) (*domain.Run, string, error) {
		run, err := s.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, task, "fake", domain.LaunchTUI)
		if err != nil {
			return nil, "", err
		}
		sc, err := s.readSidecar(run.ID)
		if err != nil {
			t.Fatalf("readSidecar: %v", err)
		}
		t.Cleanup(func() { _ = docker.Destroy(t.Context(), runtime.ID(sc.ContainerID)) })
		return run, sc.ContainerID, nil
	}

	gone, containerID, err := launch(e.sched, "merged session that loses its checkout")
	if err != nil {
		t.Fatalf("first Launch: %v", err)
	}
	if sc, err := e.sched.readSidecar(gone.ID); err != nil || sc.RunUser != unprivilegedUser {
		t.Fatalf("first session sidecar run user = %q, %v; want the reserved %q", sc.RunUser, err, unprivilegedUser)
	}
	if err = e.sched.CloseRun(ctx, gone.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.waitStoreStatus(t, gone.ID, domain.RunMerged)
	waitFor(t, "sidecar removed", func() bool {
		_, err := os.Stat(e.sched.sidecarPath(gone.ID))
		return os.IsNotExist(err)
	})

	if err = e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	if err = os.RemoveAll(e.git.checkoutPath(gone.ID)); err != nil {
		t.Fatal(err)
	}
	_ = docker.Destroy(ctx, runtime.ID(containerID))
	if _, err = cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("session container still present: %v", err)
	}
	if err = e.sched.writeSidecar(sidecar{
		RunID: string(gone.ID), WorkspaceID: string(gone.WorkspaceID),
		DestroyPending: true, EvidencePending: true, RunUser: unknownRecoveryRunUser,
	}); err != nil {
		t.Fatalf("write incident sidecar: %v", err)
	}

	recovered, err := New(e.cfg)
	if err != nil {
		t.Fatalf("New (restarted): %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.refusal = checkoutMissing(gone.ID)
	recovered.UseEvidence(capture)
	if err = recovered.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	recovered.mu.Lock()
	owner := recovered.runs[gone.ID]
	recovered.mu.Unlock()
	if owner != nil {
		t.Fatalf("recovery left an owner: %+v", owner)
	}
	if _, err = os.Stat(recovered.sidecarPath(gone.ID)); !os.IsNotExist(err) {
		t.Fatalf("sidecar after recovery: %v", err)
	}
	if row := e.waitStoreStatus(t, gone.ID, domain.RunMerged); !strings.Contains(row.Reason, "evidence unavailable: session checkout missing") {
		t.Fatalf("reason = %q, want it to record the missing checkout", row.Reason)
	}

	live, _, err := launch(recovered, "launch after the gone session")
	if err != nil {
		t.Fatalf("Launch after recovery: %v", err)
	}
	if live.Status != domain.RunRunning {
		t.Fatalf("launch after recovery status = %s, want running", live.Status)
	}

	other := unprivilegedUser
	unprivilegedUser = "2000:2000"
	t.Cleanup(func() { unprivilegedUser = other })
	_, _, err = launch(recovered, "different uid:gid beside a live session")
	if err == nil || !strings.Contains(err.Error(), "reserved by live session "+string(live.ID)+" as user "+other) {
		t.Fatalf("launch under 2000:2000 beside live %s = %v; want a reservation conflict", live.ID, err)
	}
}
