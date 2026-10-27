//go:build integration

package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/harness"
)

// plantFromRun is a "claude" argv that plants, in the member's home, every
// file the assistant used to load from there, then prints the login.
var plantFromRun = []string{"sh", "-c", `sleep 1
mkdir -p "$HOME/.claude" "$HOME/.codex" "$HOME/.local/bin"
echo planted > "$HOME/CLAUDE.md"
echo planted > "$HOME/.claude/CLAUDE.md"
echo '{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo planted"}]}]}}' > "$HOME/.claude/settings.json"
echo planted > "$HOME/.codex/AGENTS.md"
echo 'echo planted' > "$HOME/.bashrc"
printf '#!/bin/sh\necho planted\n' > "$HOME/.local/bin/claude-agent-acp"
chmod +x "$HOME/.local/bin/claude-agent-acp"
printf 'harness-login:%s\n' "$(cat "$HOME/.claude/.credentials.json")"; sleep 1`}

// TestIntegrationAssistantContainerIsolationDocker lets a run of the member
// plant agent configuration in the shared home, then starts the member's
// assistant container: it sees none of it, mounts nothing of the home but
// the login, and the run and the terminal see nothing of the assistant.
func TestIntegrationAssistantContainerIsolationDocker(t *testing.T) {
	e, docker, cli, _ := newShareDockerEnv(t, plantFromRun)
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e.sched.cfg.AssistantDir = assistantDir
	e.sched.cfg.ServerBinary = fakeServerBinary(t, "#!/bin/sh\n")
	withCoordination(t, e)
	ctx := t.Context()
	member := e.member.ID
	memberHome, err := e.cfg.Homes.Path(member)
	if err != nil {
		t.Fatal(err)
	}
	seedHome(t, memberHome, map[string]string{".claude/.credentials.json": "alice-login"})

	run := launchShareRun(t, e, docker, cli, member, member, "plant files", "alice-login")
	for _, rel := range []string{"CLAUDE.md", ".claude/settings.json", ".local/bin/claude-agent-acp"} {
		if _, err := os.Stat(filepath.Join(memberHome, rel)); err != nil {
			t.Fatalf("the run did not plant %s: %v", rel, err)
		}
	}
	for _, m := range run.mounts {
		if strings.Contains(m.Source, "/assistant") {
			t.Fatalf("the run mounts the assistant's %s at %s", m.Source, m.Destination)
		}
	}
	if code, out := run.sh(t, "grep -c aether-assistant /proc/self/mountinfo"); code != 1 {
		t.Fatalf("the run's mount table names the assistant: %s", out)
	}

	if err := os.MkdirAll(AssistantInstructionsDir(assistantDir, member), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(AssistantInstructionsDir(assistantDir, member), "CLAUDE.md"), []byte("server instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	claude, _ := harness.Lookup("claude")
	cid, err := e.sched.ensureAssistantContainer(ctx, member, claude)
	if err != nil {
		t.Fatalf("ensureAssistantContainer: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = e.sched.RemoveAssistantContainer(cctx, member)
	})
	assistant := shareRun{docker: docker, id: cid}
	assistant.want(t, `printf '%s|%s|%s' "$HOME" "$(pwd)" "$PATH"`,
		"/root|/assistant|/root/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	assistant.want(t, "cat /assistant/CLAUDE.md", "server instructions")
	if code, _ := assistant.sh(t, "echo planted > /assistant/CLAUDE.md"); code == 0 {
		t.Fatal("the assistant's instructions are writable")
	}
	for _, p := range []string{"/root/CLAUDE.md", "/root/.claude/CLAUDE.md", "/root/.claude/settings.json", "/root/.codex", "/root/.bashrc", "/root/.local/bin/claude-agent-acp"} {
		if code, out := assistant.sh(t, "[ -e "+p+" ]"); code == 0 {
			t.Errorf("%s from the member's home is visible to the assistant: %s", p, out)
		}
	}
	if code, out := assistant.sh(t, "grep -rl planted /root /assistant /opt /run /tmp"); code != 1 {
		t.Errorf("a planted file reached the assistant container: %s", out)
	}
	assistant.want(t, "cat /root/.claude/.credentials.json", "alice-login")
	assistant.want(t, `[ -d `+coordtransport.AssistantMountDir+` ] && [ -f `+coordtransport.BinaryPath+` ] && echo ok`, "ok\n")
	assistant.want(t, `printf alice-refreshed > /root/.claude/.credentials.json && cat /root/.claude/.credentials.json`, "alice-refreshed")
	if got, err := os.ReadFile(filepath.Join(memberHome, ".claude", ".credentials.json")); err != nil || string(got) != "alice-refreshed" {
		t.Errorf("a refresh in the assistant did not reach the member's home: %q, %v", got, err)
	}

	info, err := cli.ContainerInspect(ctx, string(cid), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Container.Config.Image != e.cfg.StandardImage || info.Container.Config.Tty || info.Container.HostConfig.Runtime == "sysbox-runc" {
		t.Errorf("assistant container image %q tty %v runtime %q", info.Container.Config.Image, info.Container.Config.Tty, info.Container.HostConfig.Runtime)
	}
	assistant.mounts = info.Container.Mounts
	volumes, binds := assistant.engineMounts(memberHome)
	t.Logf("assistant engine mounts: volumes %v, binds from the member's home %v", volumes, binds)
	if len(volumes) != 1 || !strings.HasSuffix(volumes[0], " at /root/.claude/.credentials.json") || len(binds) > 0 {
		t.Fatalf("assistant mounts from the member's home: volumes %v, binds %v; want only the login", volumes, binds)
	}
	for _, m := range info.Container.Mounts {
		if m.Type == mount.TypeBind && !strings.HasPrefix(m.Source, mustResolve(t, assistantDir)) && m.Destination != coordtransport.BinaryPath {
			t.Errorf("assistant bind mount from outside its directory: %s at %s", m.Source, m.Destination)
		}
	}

	if _, err := e.sched.EnsureTerminal(ctx, member); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	t.Cleanup(func() { _ = e.sched.StopTerminal(context.Background(), member) })
	sup := e.sched.lookupLiveTerminal(member)
	terminal, err := docker.Inspect(ctx, sup.containerID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(terminal.MountTargets, coordtransport.AssistantMountDir) || slices.Contains(terminal.MountTargets, coordtransport.BinaryPath) {
		t.Fatalf("the terminal mounts the assistant's socket or binary: %v", terminal.MountTargets)
	}
}

func mustResolve(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
