//go:build integration

package integration

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestRealDockerGitCombinedCandidateContent deliberately uses the production
// Docker runtime and Git engine. It does not substitute a fake runtime: a
// missing daemon is an explicit skip, while any Docker/Git execution error is
// a test failure.
func TestRealDockerGitCombinedCandidateContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	docker, err := runtime.NewDocker(runtime.WithNetworkMode("none"))
	if err != nil {
		t.Skipf("Docker runtime unavailable: %v", err)
	}
	defer docker.Close()
	if _, err := docker.ImageExists(ctx, "busybox:1.36"); err != nil {
		t.Skipf("Docker daemon unavailable: %v", err)
	}

	root := t.TempDir()
	git, err := gitengine.New(gitengine.Config{
		ReposDir: filepath.Join(root, "repos"), CheckoutsDir: filepath.Join(root, "checkouts"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer git.Close()
	ws := domain.WorkspaceID("verify-docker-git")
	repo, err := git.InitWorkspaceRepo(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(root, "source")
	gitRun(t, source, "init", "-b", "main")
	gitRun(t, source, "config", "user.name", "verification-test")
	gitRun(t, source, "config", "user.email", "verification@example.invalid")
	writeTestFile(t, filepath.Join(source, "base.txt"), "base\n")
	gitRun(t, source, "add", "base.txt")
	gitRun(t, source, "commit", "-m", "base")
	base := gitRun(t, source, "rev-parse", "HEAD")

	writeTestFile(t, filepath.Join(source, "first.txt"), "first\n")
	gitRun(t, source, "add", "first.txt")
	gitRun(t, source, "commit", "-m", "first evidence")
	first := gitRun(t, source, "rev-parse", "HEAD")
	gitRun(t, source, "update-ref", "refs/heads/main", base)
	gitRun(t, source, "reset", "--hard", base)

	writeTestFile(t, filepath.Join(source, "second.txt"), "second\n")
	gitRun(t, source, "add", "second.txt")
	gitRun(t, source, "commit", "-m", "second evidence")
	second := gitRun(t, source, "rev-parse", "HEAD")

	gitRun(t, repo, "fetch", source, first+":refs/aether/evidence/packet-one")
	gitRun(t, repo, "fetch", source, second+":refs/aether/evidence/packet-two")
	candidateID := "candidate-docker-git"
	if err := git.RetainCandidateInput(ctx, ws, candidateID, 0, "packet-one", first, base); err != nil {
		t.Fatal(err)
	}
	if err := git.RetainCandidateInput(ctx, ws, candidateID, 1, "packet-two", second, base); err != nil {
		t.Fatal(err)
	}
	if _, err := git.CandidateCheckout(ctx, ws, candidateID, base); err != nil {
		t.Fatal(err)
	}
	assembly, err := git.AssembleCandidate(ctx, ws, candidateID, []gitengine.CandidateRevisionInput{
		{Revision: first, Base: base}, {Revision: second, Base: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	if assembly.Revision == "" || assembly.AppliedInputs != 2 || len(assembly.Conflicts) != 0 {
		t.Fatalf("assembly = %+v", assembly)
	}

	verificationID := "docker-git-run"
	checkout, err := git.CandidateVerificationCheckout(ctx, ws, candidateID, verificationID, assembly.Revision)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = git.RemoveCandidateVerification(context.Background(), ws, candidateID, verificationID) }()
	id, err := docker.Create(ctx, runtime.Spec{
		Image: "busybox:1.36", WorktreeHostPath: checkout, WorktreeMountPath: "/workspace",
		WorkingDir: "/workspace", Command: []string{"sh", "-c", "cat base.txt first.txt second.txt"},
		CreationKey: "integration-test-docker-git",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = docker.Destroy(context.Background(), id) }()
	attachment, err := docker.Attach(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	outDone := make(chan struct{})
	go func() { _, _ = io.Copy(&stdout, attachment.Stdout()); close(outDone) }()
	var stderrDone = make(chan struct{})
	go func() { _, _ = io.Copy(&stderr, attachment.Stderr()); close(stderrDone) }()
	if err := docker.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	status, err := docker.Wait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	_ = attachment.Close()
	select {
	case <-outDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-stderrDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if status.Code != 0 {
		t.Fatalf("container exit=%d stderr=%q", status.Code, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "base\nfirst\nsecond"; got != want {
		t.Fatalf("combined candidate content = %q, want %q", got, want)
	}
	if err := docker.Destroy(ctx, id); err != nil {
		t.Fatal(err)
	}
	mutatedID, err := docker.Create(ctx, runtime.Spec{
		Image: "busybox:1.36", WorktreeHostPath: checkout, WorktreeMountPath: "/workspace",
		WorkingDir: "/workspace", Command: []string{"sh", "-c", "printf mutated > first.txt"},
		CreationKey: "integration-test-docker-git-mutation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := docker.Start(ctx, mutatedID); err != nil {
		t.Fatal(err)
	}
	if status, err := docker.Wait(ctx, mutatedID); err != nil || status.Code != 0 {
		t.Fatalf("mutation container status=(%+v, %v)", status, err)
	}
	if err := docker.Destroy(ctx, mutatedID); err != nil {
		t.Fatal(err)
	}
	if err := git.CheckCandidateVerification(ctx, ws, candidateID, verificationID, assembly.Revision); err == nil {
		t.Fatal("tracked source mutation was accepted")
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if args[0] == "init" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
