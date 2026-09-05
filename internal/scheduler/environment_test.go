package scheduler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestBuildEnvironmentPlanMountsOnePersistentHomeFirst(t *testing.T) {
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatalf("memberhome.New: %v", err)
	}
	s := &Scheduler{cfg: Config{StandardImage: "standard:latest", Homes: homes}}
	ws := &domain.Workspace{ID: "ws", Environment: domain.WorkspaceEnvironment{
		Variables: map[string]string{"PATH": "/workspace/bin", "EXTRA": "yes"},
	}}
	member := &domain.Member{ID: "member"}
	for _, purpose := range []EnvironmentPurpose{EnvironmentPurposeRun, EnvironmentPurposeTerminal} {
		plan, planErr := s.BuildEnvironmentPlan(context.Background(), nil, ws, member, harness.Profile{}, purpose)
		if planErr != nil {
			t.Fatalf("BuildEnvironmentPlan(%q): %v", purpose, planErr)
		}
		if len(plan.Mounts) != 1 {
			t.Fatalf("purpose %q mounts = %v, want exactly one home mount", purpose, plan.Mounts)
		}
		home, homeErr := homes.Path(member.ID)
		if homeErr != nil {
			t.Fatalf("homes.Path: %v", homeErr)
		}
		want := runtime.Mount{HostPath: home, ContainerPath: "/root"}
		if plan.Mounts[0] != want {
			t.Fatalf("purpose %q home mount = %+v, want %+v", purpose, plan.Mounts[0], want)
		}

	}
}

func TestBuildEnvironmentPlanUsesStandardImageAndToolsFirstPath(t *testing.T) {
	s := &Scheduler{cfg: Config{StandardImage: "standard:latest"}}
	ws := &domain.Workspace{ID: "ws", Environment: domain.WorkspaceEnvironment{
		Variables: map[string]string{"PATH": "/workspace/bin", "EXTRA": "yes"},
	}}
	member := &domain.Member{ID: "member"}
	plan, err := s.BuildEnvironmentPlan(context.Background(), nil, ws, member, harness.Profile{}, EnvironmentPurposeRun)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Image != "standard:latest" {
		t.Fatalf("image = %q, want standard:latest", plan.Image)
	}
	if plan.Env["PATH"] != "/root/.local/bin:/workspace/bin" {
		t.Fatalf("PATH = %q, want tools first", plan.Env["PATH"])
	}
	if plan.Env["EXTRA"] != "yes" || plan.Env["HOME"] != "/root" {
		t.Fatalf("environment = %#v", plan.Env)
	}
}
