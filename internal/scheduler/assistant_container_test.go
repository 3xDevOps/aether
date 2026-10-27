package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func writeHomeFile(t *testing.T, home, rel, content string) {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAssistantContainerMountsOnlyTheLogin plans the assistant container of a
// member whose home holds agent configuration a run could have planted: only
// the login file is mounted from that home.
func TestAssistantContainerMountsOnlyTheLogin(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
	})
	withCoordination(t, e)
	ctx := t.Context()
	memberHome, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rel, content := range map[string]string{
		".claude/.credentials.json":   `{"token":"synthetic"}`,
		".claude/settings.json":       `{"hooks":{}}`,
		".claude/CLAUDE.md":           "planted",
		"CLAUDE.md":                   "planted",
		".claude.json":                `{"projects":{}}`,
		".codex/auth.json":            `{"token":"synthetic"}`,
		".local/bin/claude-agent-acp": "#!/bin/sh\necho planted",
	} {
		writeHomeFile(t, memberHome, rel, content)
	}
	claude, _ := harness.Lookup("claude")

	cid, err := e.sched.ensureAssistantContainer(ctx, e.member.ID, claude)
	if err != nil {
		t.Fatalf("ensureAssistantContainer: %v", err)
	}
	c, err := e.rt.get(cid)
	if err != nil {
		t.Fatal(err)
	}
	spec := c.spec
	if spec.Image != e.cfg.StandardImage || spec.TTY || spec.NestedDocker {
		t.Fatalf("spec image %q tty %v nested docker %v, want the standard image without either", spec.Image, spec.TTY, spec.NestedDocker)
	}
	home := spec.Env["HOME"]
	if !strings.HasPrefix(spec.Env["PATH"], AssistantInstallPath+"/bin:") || strings.Contains(spec.Env["PATH"], home) || spec.WorkingDir != AssistantCwd {
		t.Fatalf("env %v working dir %q", spec.Env, spec.WorkingDir)
	}
	want := map[string]runtime.Mount{
		home:                                {HostPath: AssistantHomeDir(assistantDir, e.member.ID), ContainerPath: home},
		AssistantCwd:                        {HostPath: AssistantInstructionsDir(assistantDir, e.member.ID), ContainerPath: AssistantCwd, ReadOnly: true},
		AssistantInstallPath:                {HostPath: AssistantInstallDir(assistantDir, e.member.ID, "claude"), ContainerPath: AssistantInstallPath, ReadOnly: true},
		home + "/.claude/.credentials.json": {HostPath: memberHome, Subpath: ".claude/.credentials.json", ContainerPath: home + "/.claude/.credentials.json"},
		coordtransport.AssistantMountDir:    {HostPath: AssistantSocketDir(assistantDir, e.member.ID), ContainerPath: coordtransport.AssistantMountDir, ReadOnly: true},
	}
	for _, m := range spec.Mounts {
		if m.ContainerPath == coordtransport.BinaryPath || m.ContainerPath == coordtransport.CLIPath {
			if !m.ReadOnly {
				t.Errorf("server binary mounted writable")
			}
			continue
		}
		w, ok := want[m.ContainerPath]
		if !ok {
			t.Errorf("unexpected mount %+v", m)
			continue
		}
		if resolved, resolveErr := filepath.EvalSymlinks(w.HostPath); resolveErr == nil {
			w.HostPath = resolved
		}
		if m != w {
			t.Errorf("mount %+v, want %+v", m, w)
		}
		delete(want, m.ContainerPath)
	}
	if len(want) > 0 {
		t.Errorf("missing mounts %+v", want)
	}
	onboarding, err := os.ReadFile(filepath.Join(AssistantHomeDir(assistantDir, e.member.ID), ".claude.json"))
	if err != nil || string(onboarding) != `{"hasCompletedOnboarding":true}` {
		t.Errorf("assistant .claude.json = %q, %v", onboarding, err)
	}

	again, err := e.sched.ensureAssistantContainer(ctx, e.member.ID, claude)
	if err != nil || again != cid {
		t.Fatalf("second ensure = %q, %v; want the kept container %q", again, err, cid)
	}
	login := filepath.Join(memberHome, ".claude/.credentials.json")
	writeHomeFile(t, memberHome, ".claude/.credentials.json.new", `{"token":"rotated"}`)
	if err = os.Rename(login+".new", login); err != nil {
		t.Fatal(err)
	}
	replaced, err := e.sched.ensureAssistantContainer(ctx, e.member.ID, claude)
	if err != nil || replaced == cid {
		t.Fatalf("ensure after the login was replaced = %q, %v; want a new container", replaced, err)
	}
	if _, err = e.rt.get(cid); err == nil {
		t.Errorf("the container with the replaced login still exists")
	}
	if err = e.sched.RemoveAssistant(ctx, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.rt.get(replaced); err == nil {
		t.Errorf("RemoveAssistant left the container")
	}
	if _, err = os.Stat(filepath.Join(assistantDir, string(e.member.ID))); !os.IsNotExist(err) {
		t.Errorf("RemoveAssistant left the member's assistant directory: %v", err)
	}
}

// TestDiscardAssistantStateRemovesTheHomeWhole discards the assistant home
// after destroying the container, and hands the remover only the home itself:
// on a non-root server the remover empties what it is given as root, so a
// symlink the agent planted in the home must never reach it.
func TestDiscardAssistantStateRemovesTheHomeWhole(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	var removed []string
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
		c.RemoveAll = func(_ context.Context, p string) error {
			removed = append(removed, p)
			return os.RemoveAll(p)
		}
	})
	withCoordination(t, e)
	claude, _ := harness.Lookup("claude")
	cid, err := e.sched.ensureAssistantContainer(t.Context(), e.member.ID, claude)
	if err != nil {
		t.Fatal(err)
	}
	home := AssistantHomeDir(assistantDir, e.member.ID)
	outside := t.TempDir()
	writeHomeFile(t, outside, "keep.txt", "outside")
	for _, rel := range []string{
		".local/bin/node",
		".claude/projects/-assistant/session.jsonl",
		".claude/todos/a.json",
		".claude/history.jsonl",
		".codex/sessions/2026/a.jsonl",
	} {
		writeHomeFile(t, home, rel, "x")
	}
	if err = os.Symlink(outside, filepath.Join(home, "x")); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.DiscardAssistantState(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.rt.get(cid); err == nil {
		t.Error("the assistant container survived discarding its state")
	}
	if !slices.Equal(removed, []string{home}) {
		t.Fatalf("removed %v, want only the home %s", removed, home)
	}
	if _, err = os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("the assistant home survived: %v", err)
	}
	if !e.sched.hasAssistantDir(e.member.ID) {
		t.Error("after a reset the member's containers would stop pinning their logins")
	}
	if _, err = os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("discarding followed a symlink out of the home: %v", err)
	}
	if _, err = e.sched.ensureAssistantContainer(t.Context(), e.member.ID, claude); err != nil {
		t.Fatalf("ensure after discarding: %v", err)
	}
	if onboarding, err := os.ReadFile(filepath.Join(home, ".claude.json")); err != nil || string(onboarding) != `{"hasCompletedOnboarding":true}` {
		t.Errorf("onboarding after discarding = %q, %v", onboarding, err)
	}
}

// TestAssistantLoginFoundMatchesTheMounts reports a login only where the
// assistant container would mount one: a non-empty regular file in the
// member's own home.
func TestAssistantLoginFoundMatchesTheMounts(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.AssistantDir = filepath.Join(t.TempDir(), "assistant") })
	memberHome, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "auth.json")
	writeHomeFile(t, filepath.Dir(elsewhere), "auth.json", `{"token":"synthetic"}`)
	writeHomeFile(t, memberHome, ".claude/.credentials.json", "")
	if err := os.MkdirAll(filepath.Join(memberHome, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(memberHome, ".codex/auth.json")); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex"} {
		if found, err := e.sched.AssistantLoginFound(e.member.ID, agent); err != nil || found {
			t.Errorf("%s: found %v, %v; want an empty file and a symlink not to count", agent, found, err)
		}
	}
	writeHomeFile(t, memberHome, ".claude/.credentials.json", `{"token":"synthetic"}`)
	if found, err := e.sched.AssistantLoginFound(e.member.ID, "claude"); err != nil || !found {
		t.Errorf("claude: found %v, %v; want the login", found, err)
	}
}

// TestAssistantInstallsOnlyWhenMissing installs the pinned adapter in a
// container of its own that mounts the install writable, records it, keeps
// each agent's install across a switch, and installs again only when the pin
// changes, after removing an install a crashed server left running.
func TestAssistantInstallsOnlyWhenMissing(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	var e *testEnv
	var left runtime.ID
	e = newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
		c.RemoveAll = func(_ context.Context, p string) error {
			if left != "" {
				if _, err := e.rt.get(left); err == nil {
					t.Errorf("cleared %s while the leftover install container ran", p)
				}
			}
			return os.RemoveAll(p)
		}
	})
	withCoordination(t, e)
	codex, _ := harness.Lookup("codex")
	claude, _ := harness.Lookup("claude")
	install := AssistantInstallDir(assistantDir, e.member.ID, "codex")
	writeHomeFile(t, install, "bin/stale", "x")
	var installer runtime.Spec
	e.rt.execHandler = func(id runtime.ID, _ []string) (int, string, error) {
		c, err := e.rt.get(id)
		if err != nil {
			return 0, "", err
		}
		installer = c.spec
		return 0, "", nil
	}
	calls := 0
	for _, profile := range []harness.Profile{codex, codex, claude, codex} {
		if err := e.sched.installAssistant(t.Context(), e.member.ID, profile, func() { calls++ }); err != nil {
			t.Fatal(err)
		}
	}
	pkg := codex.ACPInstall.Package + "@" + codex.ACPInstall.Version
	runs := e.rt.execRuns()
	if calls != 2 || len(runs) != 2 || !slices.Equal(runs[0].argv, []string{"npm", "install", "-g", "--prefix", AssistantInstallPath, pkg}) {
		t.Fatalf("installing called %d times, execs %+v; want one install of %s and one of claude's", calls, runs, pkg)
	}
	if want := []runtime.Mount{{HostPath: AssistantInstallDir(assistantDir, e.member.ID, "claude"), ContainerPath: AssistantInstallPath}}; !slices.Equal(installer.Mounts, want) {
		t.Fatalf("installer mounts %+v, want only the install writable", installer.Mounts)
	}
	if _, err := e.rt.get(runs[0].id); err == nil {
		t.Error("the install container still exists")
	}
	if _, err := os.Stat(filepath.Join(install, "bin/stale")); !os.IsNotExist(err) {
		t.Errorf("the install did not start from an empty directory: %v", err)
	}

	var err error
	left, err = e.rt.Create(t.Context(), runtime.Spec{Name: "left", Image: "busybox", Command: assistantIdle, CreationKey: assistantInstallCreationKey(e.member.ID)})
	if err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, install, "bin/old", "x")
	if err = os.WriteFile(assistantInstalled(assistantDir, e.member.ID, "codex"), []byte(codex.ACPInstall.Package+"@0.0.1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.installAssistant(t.Context(), e.member.ID, codex, func() {}); err != nil {
		t.Fatalf("install after the pin moved: %v", err)
	}
	if _, err = e.rt.get(left); err == nil {
		t.Error("the leftover install container survived")
	}
	if n := len(e.rt.execRuns()); n != 3 {
		t.Errorf("execs after the pin moved = %d, want a third install", n)
	}
	if _, err = os.Stat(filepath.Join(install, "bin/old")); !os.IsNotExist(err) {
		t.Errorf("the reinstall kept the old install: %v", err)
	}
	left = ""
	stray, err := e.rt.Create(t.Context(), runtime.Spec{Name: "stray", Image: "busybox", Command: assistantIdle, CreationKey: assistantInstallCreationKey(e.member.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.sched.RemoveAssistant(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.rt.get(stray); err == nil {
		t.Error("RemoveAssistant left the install container")
	}
}

// TestAssistantInstallCancelledDestroysTheContainer stops an install whose
// exec was cancelled and records nothing, so a retry starts over.
func TestAssistantInstallCancelledDestroysTheContainer(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
	})
	withCoordination(t, e)
	claude, _ := harness.Lookup("claude")
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) { return 0, "", context.Canceled }
	if err := e.sched.installAssistant(t.Context(), e.member.ID, claude, func() {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("installAssistant = %v, want the cancellation", err)
	}
	if _, err := e.rt.get(e.rt.execRuns()[0].id); err == nil {
		t.Fatal("the container of a cancelled install still exists")
	}
	if _, err := os.Stat(assistantInstalled(assistantDir, e.member.ID, "claude")); !os.IsNotExist(err) {
		t.Fatalf("a cancelled install was recorded: %v", err)
	}
}
