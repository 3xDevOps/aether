package scheduler

import (
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
		".claude/.credentials.json": `{"token":"synthetic"}`,
		".claude/settings.json":     `{"hooks":{}}`,
		".claude/CLAUDE.md":         "planted",
		"CLAUDE.md":                 "planted",
		".claude.json":              `{"projects":{}}`,
		".codex/auth.json":          `{"token":"synthetic"}`,
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
	if !strings.HasPrefix(spec.Env["PATH"], home+"/.local/bin:") || spec.WorkingDir != AssistantCwd {
		t.Fatalf("env %v working dir %q", spec.Env, spec.WorkingDir)
	}
	want := map[string]runtime.Mount{
		home:                                {HostPath: AssistantHomeDir(assistantDir, e.member.ID), ContainerPath: home},
		AssistantCwd:                        {HostPath: AssistantInstructionsDir(assistantDir, e.member.ID), ContainerPath: AssistantCwd, ReadOnly: true},
		home + "/.claude/.credentials.json": {HostPath: memberHome, Subpath: ".claude/.credentials.json", ContainerPath: home + "/.claude/.credentials.json"},
		coordtransport.AssistantMountDir:    {HostPath: AssistantSocketDir(assistantDir, e.member.ID), ContainerPath: coordtransport.AssistantMountDir, ReadOnly: true},
	}
	for _, m := range spec.Mounts {
		if m.ContainerPath == coordtransport.BinaryPath {
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
	if _, err := e.rt.get(cid); err == nil {
		t.Errorf("the container with the replaced login still exists")
	}
	if err := e.sched.RemoveAssistantContainer(ctx, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rt.get(replaced); err == nil {
		t.Errorf("RemoveAssistantContainer left the container")
	}
}

// TestAssistantInstallsOnlyWhenMissing installs the agent in the assistant
// container when the check fails, and never otherwise.
func TestAssistantInstallsOnlyWhenMissing(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = filepath.Join(t.TempDir(), "assistant")
	})
	withCoordination(t, e)
	codex, _ := harness.Lookup("codex")
	cid, err := e.sched.ensureAssistantContainer(t.Context(), e.member.ID, codex)
	if err != nil {
		t.Fatal(err)
	}
	installed := false
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if installed || strings.Contains(argv[2], "npm install") {
			installed = true
			return 0, "", nil
		}
		return 1, "", nil
	}
	calls := 0
	for range 2 {
		if err := e.sched.installAssistant(t.Context(), cid, codex, func() { calls++ }); err != nil {
			t.Fatal(err)
		}
	}
	runs := e.rt.execRuns()
	if calls != 1 || len(runs) != 3 || !slices.Contains(runs[1].argv, codex.InstallCommand(true)) {
		t.Fatalf("installing called %d times, execs %+v; want one install between two checks", calls, runs)
	}
}
