//go:build linux

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// The test binary stands in for the server's package-exchange subcommand,
// without intercepting other scheduler tests through TestMain.
func TestHarnessUpdateMountExchangeHelper(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 {
		return
	}
	args := os.Args[i+1:]
	if len(args) != 3 || args[0] != "package-exchange" {
		t.Fatalf("unexpected helper arguments: %q", args)
	}
	if err := harness.ExchangePackages(args[1], args[2]); err != nil {
		t.Fatal(err)
	}
}

type mountConsumerRuntime struct {
	*fakeRuntime
}

func (r *mountConsumerRuntime) Create(ctx context.Context, spec runtime.Spec) (runtime.ID, error) {
	spec.Mounts = slices.Clone(spec.Mounts)
	for i, mount := range spec.Mounts {
		// Resolve in a different executable, like the Docker daemon. A raw
		// /proc/self/exe now names readlink, not the scheduler's helper.
		out, err := exec.CommandContext(ctx, "readlink", "-f", mount.HostPath).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("consumer resolves mount: %w: %s", err, out)
		}
		spec.Mounts[i].HostPath = strings.TrimSpace(string(out))
	}
	return r.fakeRuntime.Create(ctx, spec)
}

func (r *mountConsumerRuntime) Exec(ctx context.Context, id runtime.ID, argv []string, workDir string) (int, string, string, error) {
	container, err := r.get(id)
	if err != nil {
		return 0, "", "", err
	}
	argv = slices.Clone(argv)
	for i, arg := range argv {
		for _, mount := range container.spec.Mounts {
			arg = strings.ReplaceAll(arg, mount.ContainerPath, mount.HostPath)
		}
		argv[i] = arg
	}
	if !filepath.IsAbs(argv[0]) {
		argv[0] = filepath.Join(workDir, ".local", "bin", argv[0])
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), string(out), "", nil
	}
	return 0, string(out), "", err
}

func TestHarnessUpdateDefaultBinaryAcrossProcesses(t *testing.T) {
	t.Parallel()
	rt := &mountConsumerRuntime{fakeRuntime: newFakeRuntime()}
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = rt
	})
	installInHome(t, e, "agent")
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(home, "installed")
	staged := filepath.Join(home, "staged")
	for path, version := range map[string]string{installed: "1.0.0", staged: "2.0.0"} {
		if err = os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(path, "version"), []byte(version), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	agent := filepath.Join(home, ".local", "bin", "agent")
	if err = os.WriteFile(agent, []byte(fmt.Sprintf("#!/bin/sh\ncat %q\n", filepath.Join(installed, "version"))), 0o755); err != nil {
		t.Fatal(err)
	}
	version := func(want string) {
		t.Helper()
		out, execErr := exec.CommandContext(t.Context(), agent, "--version").CombinedOutput()
		if execErr != nil || string(out) != want {
			t.Fatalf("installed agent version = %q, %v; want %q", out, execErr, want)
		}
	}
	version("1.0.0")
	profile := harness.Profile{
		Name:         "mount-regression",
		TUIArgs:      []string{"agent"},
		UpdateScript: fmt.Sprintf("%q -test.run=^TestHarnessUpdateMountExchangeHelper$ -- package-exchange %q %q", agentstatus.ReporterCommand, installed, staged),
	}
	plan := &EnvironmentPlan{
		Image:  e.cfg.StandardImage,
		Home:   home,
		Mounts: []runtime.Mount{{HostPath: home, ContainerPath: home}},
	}
	e.sched.updateHarness(t.Context(), &domain.Run{WorkspaceID: e.ws.ID, ID: "mount-regression"}, plan, profile)
	version("2.0.0")
	old, err := os.ReadFile(filepath.Join(staged, "version"))
	if err != nil || string(old) != "1.0.0" {
		t.Fatalf("exchanged previous package = %q, %v; want 1.0.0", old, err)
	}
}
