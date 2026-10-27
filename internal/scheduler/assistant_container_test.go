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
	"github.com/3xDevOps/Aether/internal/ptyhost"
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

// TestAssistantContainerMountsNothingFromTheMemberHome plans the assistant
// container of a member whose home holds a sign-in and agent configuration
// their runs could have written: none of it is mounted.
func TestAssistantContainerMountsNothingFromTheMemberHome(t *testing.T) {
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
		".claude/.credentials.json":   `{"claudeAiOauth":{"accessToken":"synthetic"}}`,
		".claude/settings.json":       `{"hooks":{}}`,
		".codex/auth.json":            `{"OPENAI_API_KEY":"synthetic"}`,
		".local/bin/claude-agent-acp": "#!/bin/sh\necho planted",
	} {
		writeHomeFile(t, memberHome, rel, content)
	}

	cid, err := e.sched.ensureAssistantContainer(ctx, e.member.ID)
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
	if want := AssistantInstallPath + "/claude/node_modules/.bin:" + AssistantInstallPath + "/codex/node_modules/.bin:" + assistantPath; spec.Env["PATH"] != want || spec.WorkingDir != AssistantCwd {
		t.Fatalf("PATH %q working dir %q; want %q in %s", spec.Env["PATH"], spec.WorkingDir, want, AssistantCwd)
	}
	member := filepath.Join(assistantDir, string(e.member.ID))
	want := map[string]runtime.Mount{
		home:                             {HostPath: AssistantHomeDir(assistantDir, e.member.ID), ContainerPath: home},
		AssistantCwd:                     {HostPath: AssistantInstructionsDir(assistantDir, e.member.ID), ContainerPath: AssistantCwd, ReadOnly: true},
		AssistantInstallPath:             {HostPath: filepath.Join(member, "install"), ContainerPath: AssistantInstallPath, ReadOnly: true},
		"/etc/claude-code":               {HostPath: filepath.Join(member, "policy"), ContainerPath: "/etc/claude-code", ReadOnly: true},
		"/etc/codex":                     {HostPath: filepath.Join(member, "policy"), ContainerPath: "/etc/codex", ReadOnly: true},
		coordtransport.AssistantMountDir: {HostPath: AssistantSocketDir(assistantDir, e.member.ID), ContainerPath: coordtransport.AssistantMountDir, ReadOnly: true},
	}
	for _, m := range spec.Mounts {
		if strings.HasPrefix(m.HostPath, e.cfg.Homes.Root()) {
			t.Errorf("mounts %s from the member homes at %s", m.HostPath, m.ContainerPath)
		}
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
	if found, loginErr := e.sched.AssistantLoginFound(e.member.ID, "claude"); loginErr != nil || found {
		t.Errorf("the member's own sign-in made the assistant ready: %v, %v", found, loginErr)
	}

	again, err := e.sched.ensureAssistantContainer(ctx, e.member.ID)
	if err != nil || again != cid {
		t.Fatalf("second ensure = %q, %v; want the kept container %q", again, err, cid)
	}
	if err = e.sched.RemoveAssistant(ctx, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.rt.get(cid); err == nil {
		t.Errorf("RemoveAssistant left the container")
	}
	if _, err = os.Stat(member); !os.IsNotExist(err) {
		t.Errorf("RemoveAssistant left the member's assistant directory: %v", err)
	}
}

// TestPrepareAssistantHomeKeepsOnlySignInsAndSessions plants what a steered
// assistant could write into its home to steer the next session: a start
// keeps only each agent's sign-in and session files.
func TestPrepareAssistantHomeKeepsOnlySignInsAndSessions(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) { c.AssistantDir = assistantDir })
	home := AssistantHomeDir(assistantDir, e.member.ID)
	outside := t.TempDir()
	writeHomeFile(t, outside, "keep.txt", "outside")
	kept := []string{
		".claude/.credentials.json",
		".claude/projects/-assistant/s.jsonl",
		".codex/auth.json",
		".codex/sessions/2026/a.jsonl",
		".codex/session_index.jsonl",
		".codex/state_5.sqlite",
	}
	planted := []string{
		".claude.json",
		".claude/settings.json",
		".claude/CLAUDE.md",
		".claude/agents/a.md",
		".claude/todos/a.json",
		".codex/config.toml",
		".codex/AGENTS.md",
		".codex/memories/a.md",
		".local/bin/node",
		".bashrc",
		"CLAUDE.md",
	}
	for _, rel := range append(slices.Clone(kept), planted...) {
		content := "x"
		if strings.HasSuffix(rel, ".credentials.json") || strings.HasSuffix(rel, "auth.json") {
			content = "{}"
		}
		writeHomeFile(t, home, rel, content)
	}
	if err := os.Symlink(outside, filepath.Join(home, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".codex/session_index.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "keep.txt"), filepath.Join(home, ".codex/session_index.jsonl")); err != nil {
		t.Fatal(err)
	}
	removed, err := e.sched.prepareAssistantHome(t.Context(), e.member.ID)
	if err != nil || len(removed) != 0 {
		t.Fatalf("prepareAssistantHome = %v, %v", removed, err)
	}
	var left []string
	err = filepath.WalkDir(home, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr == nil && !d.IsDir() {
			rel, _ := filepath.Rel(home, p)
			left = append(left, rel)
		}
		return walkErr
	})
	if err != nil {
		t.Fatal(err)
	}
	want := append(slices.DeleteFunc(slices.Clone(kept), func(rel string) bool { return rel == ".codex/session_index.jsonl" }), ".claude.json")
	slices.Sort(want)
	slices.Sort(left)
	if !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
	if onboarding, readErr := os.ReadFile(filepath.Join(home, ".claude.json")); readErr != nil || string(onboarding) != `{"hasCompletedOnboarding":true}` {
		t.Errorf(".claude.json = %q, %v; want only the onboarding state", onboarding, readErr)
	}
	if _, err = os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("preparing followed a symlink out of the home: %v", err)
	}
}

// TestSanitizeAssistantLogin rewrites each sign-in file to the keys the
// assistant keeps: no gateway, MCP tokens or other auth modes survive, and
// the start that found them is refused once.
func TestSanitizeAssistantLogin(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
	})
	withCoordination(t, e)
	home := AssistantHomeDir(assistantDir, e.member.ID)
	writeHomeFile(t, home, ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"synthetic","refreshToken":"synthetic","baseUrl":"https://gateway.invalid"},`+
		`"enterpriseGateway":{"url":"https://gateway.invalid","jwt":"synthetic"},"gatewayTrust":true,"mcpOAuth":{"x":{}}}`)
	writeHomeFile(t, home, ".codex/auth.json", `{"auth_mode":"bedrock_iam","OPENAI_API_KEY":null,"tokens":{"access_token":"synthetic","issuer":"https://gateway.invalid"},"last_refresh":"2026-10-27T00:00:00Z","agent_identity":{}}`)

	_, err := e.sched.freshAssistantContainer(t.Context(), e.member.ID, "claude", func() {}, true)
	for _, key := range []string{"enterpriseGateway", "gatewayTrust", "mcpOAuth", "claudeAiOauth.baseUrl", `auth_mode "bedrock_iam"`, "agent_identity", "tokens.issuer"} {
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("start error %v does not name %s", err, key)
		}
	}
	for rel, want := range map[string]string{
		".claude/.credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic","refreshToken":"synthetic"}}`,
		".codex/auth.json":          `{"OPENAI_API_KEY":null,"last_refresh":"2026-10-27T00:00:00Z","tokens":{"access_token":"synthetic"}}`,
	} {
		if got, readErr := os.ReadFile(filepath.Join(home, rel)); readErr != nil || string(got) != want {
			t.Errorf("%s = %s, %v; want %s", rel, got, readErr, want)
		}
	}
	if _, err = e.sched.freshAssistantContainer(t.Context(), e.member.ID, "claude", func() {}, true); err != nil {
		t.Fatalf("the start after the cleanup: %v", err)
	}
	if found, err := e.sched.AssistantLoginFound(e.member.ID, "claude"); err != nil || !found {
		t.Errorf("AssistantLoginFound = %v, %v; want the assistant's own sign-in", found, err)
	}
}

// TestAssistantLoginRunsInTheAssistantContainer starts a sign-in on a PTY
// session in the assistant container, with the adapter's own CLI.
func TestAssistantLoginRunsInTheAssistantContainer(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
	})
	withCoordination(t, e)
	key, err := e.sched.StartAssistantLogin(t.Context(), e.member.ID, "codex", 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	if key != ptyhost.AssistantLoginSession(e.member.ID, "codex") {
		t.Fatalf("session %q", key)
	}
	cid, err := e.rt.FindByCreationKey(t.Context(), assistantCreationKey(e.member.ID))
	if err != nil {
		t.Fatal(err)
	}
	calls := e.rt.execTTYCalls()
	if len(calls) != 1 || calls[0].id != cid || !slices.Equal(calls[0].argv, []string{"codex", "login", "--device-auth"}) || calls[0].workDir != AssistantCwd {
		t.Fatalf("sign-in execs %+v, want codex login --device-auth in the assistant container %s", calls, cid)
	}
	if _, err = e.sched.StartAssistantLogin(t.Context(), e.member.ID, "codex", 100, 30); err != nil || len(e.rt.execTTYCalls()) != 1 {
		t.Fatalf("a second sign-in while one runs started another: %v, %d execs", err, len(e.rt.execTTYCalls()))
	}
	if _, err = e.sched.StartAssistantLogin(t.Context(), e.member.ID, "pi", 100, 30); err == nil {
		t.Fatal("signed in an agent the assistant does not run")
	}
}

// TestDiscardAssistantStateKeepsOnlyTheSignIns discards everything in the
// assistant home but the sign-in files, after destroying the container. On a
// non-root server the remover empties what it is given as root, so it gets
// only real directories, never a symlink the agent planted.
func TestDiscardAssistantStateKeepsOnlyTheSignIns(t *testing.T) {
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
	cid, err := e.sched.ensureAssistantContainer(t.Context(), e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	home := AssistantHomeDir(assistantDir, e.member.ID)
	outside := t.TempDir()
	writeHomeFile(t, outside, "keep.txt", "outside")
	for _, rel := range []string{
		".claude/.credentials.json",
		".claude/projects/-assistant/session.jsonl",
		".claude/todos/a.json",
		".claude/history.jsonl",
		".codex/sessions/2026/a.jsonl",
		".local/bin/node",
	} {
		writeHomeFile(t, home, rel, "x")
	}
	links := []string{filepath.Join(home, "x"), filepath.Join(home, ".codex", "auth.json"), filepath.Join(home, ".claude", "projects", "-assistant", "y")}
	for _, link := range links {
		if err = os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
	}
	if err = e.sched.DiscardAssistantState(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.rt.get(cid); err == nil {
		t.Error("the assistant container survived discarding its state")
	}
	for _, p := range removed {
		if slices.Contains(links, p) {
			t.Errorf("handed the symlink %s to the remover", p)
		}
	}
	var left []string
	err = filepath.WalkDir(home, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr == nil && !d.IsDir() {
			rel, _ := filepath.Rel(home, p)
			left = append(left, rel)
		}
		return walkErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".claude/.credentials.json"}; !slices.Equal(left, want) {
		t.Fatalf("left %v, want only the sign-in %v", left, want)
	}
	if _, err = os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("discarding followed a symlink out of the home: %v", err)
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
	runs := e.rt.execRuns()
	if calls != 2 || len(runs) != 2 || !slices.Equal(runs[0].argv, []string{"npm", "install", "--no-audit", "--no-fund", "--prefix", AssistantInstallPath + "/codex"}) {
		t.Fatalf("installing called %d times, execs %+v; want one install for codex and one for claude", calls, runs)
	}
	manifest, err := os.ReadFile(filepath.Join(install, "package.json"))
	if want := `{"dependencies":{"@agentclientprotocol/codex-acp":"` + codex.ACPInstall.Version + `","@openai/codex":"0.159.3"},"private":true}`; err != nil || string(manifest) != want {
		t.Fatalf("codex package.json = %s, %v; want %s", manifest, err, want)
	}
	if want := []runtime.Mount{{HostPath: AssistantInstallDir(assistantDir, e.member.ID, "claude"), ContainerPath: AssistantInstallPath + "/claude"}}; !slices.Equal(installer.Mounts, want) {
		t.Fatalf("installer mounts %+v, want only the install writable", installer.Mounts)
	}
	if _, err = e.rt.get(runs[0].id); err == nil {
		t.Error("the install container still exists")
	}
	if _, err = os.Stat(filepath.Join(install, "bin/stale")); !os.IsNotExist(err) {
		t.Errorf("the install did not start from an empty directory: %v", err)
	}

	left, err = e.rt.Create(t.Context(), runtime.Spec{Name: "left", Image: "busybox", Command: assistantIdle, CreationKey: assistantInstallCreationKey(e.member.ID)})
	if err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, install, "bin/old", "x")
	if err = os.WriteFile(assistantInstalled(assistantDir, e.member.ID, "codex"), []byte(`{"dependencies":{"@agentclientprotocol/codex-acp":"0.0.1"}}`), 0o644); err != nil {
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
