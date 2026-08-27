//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// envDockerfile is the tiny definition the lifecycle test builds: the
// mandatory base image plus one apt package pinned to its upstream series
// (the packaging revision changes with security updates, so an exact pin
// would rot).
const envDockerfile = `FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends 'jq=1.7*' \
 && rm -rf /var/lib/apt/lists/*
`

// envManifest is the matching one-item manifest; version is the string
// the check command's output must contain.
func envManifest(t *testing.T, version string) json.RawMessage {
	t.Helper()
	items := []domain.ManifestItem{{
		Name:         "jq",
		Version:      version,
		Reason:       "integration lifecycle check",
		StartLine:    2,
		EndLine:      3,
		CheckCommand: "jq --version",
	}}
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return data
}

// TestIntegrationEnvironmentLifecycle proves the phase 1 environment
// foundation end to end over one wired server and a real Docker daemon:
// env.save stores a definition, env.build builds and verifies it and
// swaps the workspace image, a run container starts from the swapped
// image, a wrong manifest version fails verification without touching the
// image, a later good version swaps again and retention prunes the failed
// tag, and env.rollback re-activates version 1. The in-process fallback
// runtime cannot build images, so this test requires the real daemon.
func TestIntegrationEnvironmentLifecycle(t *testing.T) {
	requireBinary(t, "git")
	requireDockerDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("runtime.NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	cli := newDockerCLI(t)
	label := "aether.test=" + t.Name()
	removeLabeledContainers(t, cli, label)
	t.Cleanup(func() { removeLabeledContainers(t, cli, label) })

	dataDir := filepath.Join(t.TempDir(), "data")
	srv, err := New(ctx, Config{DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: docker})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	keyPath, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Env Tester",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#e6194b",
		Role:        domain.RoleAdmin,
	}
	ws := &domain.Workspace{
		Name:        "env-e2e",
		Environment: domain.WorkspaceEnvironment{CustomImage: "busybox"},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err = srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err = srv.Store().CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	envRepo := "aether/ws-" + string(ws.ID)
	t.Cleanup(func() { removeEnvironmentImages(t, cli, envRepo) })

	sub, err := srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatalf("subscribe bus: %v", err)
	}
	defer func() { _ = sub.Close() }()
	var seen []events.Event

	runDone := make(chan error, 1)
	runCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	go func() { runDone <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)

	// Seed the workspace repo so a run can launch after the image swap.
	seedDir := t.TempDir()
	repoURL := fmt.Sprintf("ssh://aether@%s/%s.git", addr, ws.ID)
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "Env")
	runGit(t, seedDir, gitEnv, "config", "user.email", "env@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), agentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	sshClient := dialSSH(t, addr, signer)
	ctrl := openControl(t, sshClient)
	sel := protocol.WorkspaceSelector{ID: string(ws.ID)}

	saveEnv := func(desc string, manifest json.RawMessage) int {
		t.Helper()
		var res protocol.EnvSaveResult
		if err := ctrl.Call(protocol.MethodEnvSave, protocol.EnvSaveParams{
			Workspace:  sel,
			Dockerfile: envDockerfile,
			Manifest:   manifest,
			Source:     string(domain.EnvironmentSourceManual),
		}, &res); err != nil {
			t.Fatalf("env.save %s: %v", desc, err)
		}
		return res.Version
	}
	buildEnv := func(version int) events.Event {
		t.Helper()
		var res protocol.EnvBuildResult
		if err := ctrl.Call(protocol.MethodEnvBuild, protocol.EnvBuildParams{
			Workspace: sel, Version: version,
		}, &res); err != nil {
			t.Fatalf("env.build v%d: %v", version, err)
		}
		if res.Version != version {
			t.Fatalf("env.build launched version %d, want %d", res.Version, version)
		}
		return waitEvent(t, sub, &seen, fmt.Sprintf("environment.build v%d terminal status", version),
			func(e events.Event) bool {
				p, ok := e.Payload.(events.EnvironmentBuildPayload)
				return ok && e.WorkspaceID == ws.ID && p.Version == version &&
					(p.Status == domain.EnvironmentActive || p.Status == domain.EnvironmentFailed)
			})
	}
	envStatus := func() protocol.EnvStatusResult {
		t.Helper()
		var res protocol.EnvStatusResult
		if err := ctrl.Call(protocol.MethodEnvStatus, protocol.EnvStatusParams{Workspace: sel}, &res); err != nil {
			t.Fatalf("env.status: %v", err)
		}
		return res
	}
	workspaceImage := func() string {
		t.Helper()
		w, err := srv.Store().GetWorkspace(ctx, ws.ID)
		if err != nil {
			t.Fatalf("get workspace: %v", err)
		}
		return w.Environment.CustomImage
	}

	// Version 1: save, build, verify, activate, swap.
	if v := saveEnv("v1", envManifest(t, "1.7")); v != 1 {
		t.Fatalf("first save assigned version %d, want 1", v)
	}
	if ev := buildEnv(1); ev.Payload.(events.EnvironmentBuildPayload).Status != domain.EnvironmentActive {
		t.Fatalf("v1 terminal status = %+v, want active", ev.Payload)
	}
	waitEvent(t, sub, &seen, "v1 build progress line", func(e events.Event) bool {
		p, ok := e.Payload.(events.EnvironmentBuildPayload)
		return ok && e.WorkspaceID == ws.ID && p.Version == 1 &&
			p.Status == domain.EnvironmentBuilding && p.Line != ""
	})
	tag1 := envRepo + ":1"
	if got := workspaceImage(); got != tag1 {
		t.Fatalf("workspace image after v1 = %q, want %q", got, tag1)
	}
	if st := envStatus(); st.ActiveVersion != 1 {
		t.Fatalf("env.status active = %d, want 1", st.ActiveVersion)
	}

	// A run container starts from the swapped image.
	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/agent.sh")
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(ws.ID), Task: "env lifecycle e2e", Harness: "fake",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	att := openAttach(t, sshClient, launched.Run.ID)
	att.waitOutput(t, "agent-ready")
	if img := runContainerImage(t, cli, label); img != tag1 {
		t.Fatalf("run container image = %q, want %q", img, tag1)
	}
	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: launched.Run.ID, Message: "done",
	}, nil); err != nil {
		t.Fatalf("run.inject: %v", err)
	}
	att.waitEnd(t)
	waitEvent(t, sub, &seen, "run parked after agent exit", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && e.RunID == domain.RunID(launched.Run.ID) && p.To == domain.RunNeedsAttention
	})

	// Version 2 declares a version jq does not report: the build succeeds
	// but verification fails, and the workspace image stays on v1.
	if v := saveEnv("v2", envManifest(t, "9.9.9")); v != 2 {
		t.Fatalf("second save assigned version %d, want 2", v)
	}
	ev := buildEnv(2)
	failed := ev.Payload.(events.EnvironmentBuildPayload)
	if failed.Status != domain.EnvironmentFailed {
		t.Fatalf("v2 terminal status = %+v, want failed", failed)
	}
	if !strings.Contains(failed.Detail, `item "jq"`) || !strings.Contains(failed.Detail, "9.9.9") {
		t.Fatalf("v2 failure detail = %q, want the mismatched item named", failed.Detail)
	}
	if got := workspaceImage(); got != tag1 {
		t.Fatalf("workspace image after failed v2 = %q, want %q", got, tag1)
	}
	st := envStatus()
	if st.ActiveVersion != 1 || len(st.Versions) != 2 {
		t.Fatalf("env.status after failed v2 = %+v", st)
	}
	if v2 := st.Versions[0]; v2.Version != 2 || v2.Status != domain.EnvironmentFailed || v2.FailureDetail == "" {
		t.Fatalf("newest-first version row = %+v, want failed v2 with detail", v2)
	}

	// Version 3 is good again: it activates, swaps the image, and
	// retention prunes the failed v2 tag (active v3 + previous v1 stay).
	if v := saveEnv("v3", envManifest(t, "1.7")); v != 3 {
		t.Fatalf("third save assigned version %d, want 3", v)
	}
	if ev := buildEnv(3); ev.Payload.(events.EnvironmentBuildPayload).Status != domain.EnvironmentActive {
		t.Fatalf("v3 terminal status = %+v, want active", ev.Payload)
	}
	tag3 := envRepo + ":3"
	if got := workspaceImage(); got != tag3 {
		t.Fatalf("workspace image after v3 = %q, want %q", got, tag3)
	}
	if got, want := environmentTags(t, cli, envRepo), []string{tag1, tag3}; !equalStrings(got, want) {
		t.Fatalf("retained tags after v3 = %v, want %v", got, want)
	}

	// Rollback re-activates version 1 (its tag survived retention, so no
	// rebuild is needed) and swaps the workspace image back.
	var rb protocol.EnvRollbackResult
	if err := ctrl.Call(protocol.MethodEnvRollback, protocol.EnvRollbackParams{Workspace: sel}, &rb); err != nil {
		t.Fatalf("env.rollback: %v", err)
	}
	if rb.Version != 1 {
		t.Fatalf("env.rollback version = %d, want 1", rb.Version)
	}
	if got := workspaceImage(); got != tag1 {
		t.Fatalf("workspace image after rollback = %q, want %q", got, tag1)
	}
	if st := envStatus(); st.ActiveVersion != 1 {
		t.Fatalf("env.status active after rollback = %d, want 1", st.ActiveVersion)
	}
	if got, want := environmentTags(t, cli, envRepo), []string{tag1, tag3}; !equalStrings(got, want) {
		t.Fatalf("retained tags after rollback = %v, want %v", got, want)
	}

	stopServer()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("server.Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("server did not shut down")
	}
	if leaked := labeledContainers(t, cli, label); len(leaked) > 0 {
		t.Errorf("containers leaked after clean shutdown: %v", leaked)
	}
}

// runContainerImage returns the image of the single live test-labeled
// container: the run container, once verification's throwaway container
// is gone.
func runContainerImage(t *testing.T, cli *client.Client, label string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	list, err := cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", label)),
	})
	if err != nil {
		t.Fatalf("list run containers: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("live labeled containers = %d, want the run container alone", len(list))
	}
	return list[0].Image
}

// environmentTags lists the workspace's locally built environment tags,
// sorted, so retention assertions are order-independent.
func environmentTags(t *testing.T, cli *client.Client, repo string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sums, err := cli.ImageList(ctx, image.ListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", repo)),
	})
	if err != nil {
		t.Fatalf("list environment images: %v", err)
	}
	var tags []string
	for _, s := range sums {
		for _, tag := range s.RepoTags {
			if strings.HasPrefix(tag, repo+":") {
				tags = append(tags, tag)
			}
		}
	}
	sort.Strings(tags)
	return tags
}

// removeEnvironmentImages force-removes every tag the test built so an
// interrupted run does not leave workspace images on the daemon.
func removeEnvironmentImages(t *testing.T, cli *client.Client, repo string) {
	t.Helper()
	for _, tag := range environmentTags(t, cli, repo) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if _, err := cli.ImageRemove(ctx, tag, image.RemoveOptions{Force: true}); err != nil {
			t.Logf("remove environment image %s: %v", tag, err)
		}
		cancel()
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
