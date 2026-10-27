//go:build integration

package scheduler

import (
	"bufio"
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
	"github.com/3xDevOps/Aether/internal/runtime"
)

// plantFromRun is a "claude" argv that plants, in the member's home, every
// file the assistant used to load from there, then prints the login.
var plantFromRun = []string{"sh", "-c", `sleep 1
mkdir -p "$HOME/.claude" "$HOME/.codex" "$HOME/.local/bin"
echo run-planted-7c1 > "$HOME/CLAUDE.md"
echo run-planted-7c1 > "$HOME/.claude/CLAUDE.md"
echo '{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo run-planted-7c1"}]}]}}' > "$HOME/.claude/settings.json"
echo run-planted-7c1 > "$HOME/.codex/AGENTS.md"
echo 'echo run-planted-7c1' > "$HOME/.bashrc"
printf '#!/bin/sh\necho run-planted-7c1\n' > "$HOME/.local/bin/claude-agent-acp"
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
	e.sched.cfg.ServerBinary = buildStatic(t, "./cmd/aether-server", "aether-server")
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
		_ = e.sched.RemoveAssistant(cctx, member)
	})
	assistant := shareRun{docker: docker, id: cid}
	assistant.want(t, `printf '%s|%s|%s' "$HOME" "$(pwd)" "$PATH"`,
		"/root|/assistant|/root/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	assistant.want(t, "cat /assistant/CLAUDE.md", "server instructions")
	if code, _ := assistant.sh(t, "echo run-planted-7c1 > /assistant/CLAUDE.md"); code == 0 {
		t.Fatal("the assistant's instructions are writable")
	}
	for _, p := range []string{"/root/CLAUDE.md", "/root/.claude/CLAUDE.md", "/root/.claude/settings.json", "/root/.codex", "/root/.bashrc", "/root/.local/bin/claude-agent-acp"} {
		if code, out := assistant.sh(t, "[ -e "+p+" ]"); code == 0 {
			t.Errorf("%s from the member's home is visible to the assistant: %s", p, out)
		}
	}
	if code, out := assistant.sh(t, "grep -rl run-planted-7c1 /root /assistant /run /tmp"); code != 1 {
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
	resolvedDir, err := filepath.EvalSymlinks(assistantDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range info.Container.Mounts {
		if m.Type == mount.TypeBind && !strings.HasPrefix(m.Source, resolvedDir) && m.Destination != coordtransport.BinaryPath && m.Destination != coordtransport.CLIPath {
			t.Errorf("assistant bind mount from outside its directory: %s at %s", m.Source, m.Destination)
		}
	}

	assistantHome := AssistantHomeDir(assistantDir, member)
	seedHome(t, assistantHome, map[string]string{
		".local/bin/claude":           "#!/bin/sh\n",
		".local/bin/node":             "#!/bin/sh\necho " + claude.ACPInstall.Version + "\n",
		".local/bin/claude-agent-acp": "#!/bin/sh\necho \"adapter in $(pwd) as $IS_SANDBOX\"\nexec cat\n",
	})
	for _, name := range []string{"claude", "node", "claude-agent-acp"} {
		if err := os.Chmod(filepath.Join(assistantHome, ".local", "bin", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exec, err := e.sched.StartAssistantExec(ctx, member, "claude", runtime.ExecSpec{
		Argv: claude.ACPArgs, CreationKey: "assistant-integration", Env: []string{"IS_SANDBOX=1"},
	}, func() { t.Error("the installed agent was installed again") })
	if err != nil {
		t.Fatalf("StartAssistantExec: %v", err)
	}
	line, err := bufio.NewReader(exec.Attachment().Stdout()).ReadString('\n')
	if err != nil || line != "adapter in /assistant as 1\n" {
		t.Fatalf("adapter said %q, %v", line, err)
	}
	if exec.Identity().ContainerID != cid {
		t.Fatalf("the adapter runs in %s, want the assistant container %s", exec.Identity().ContainerID, cid)
	}
	stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
	_, err = exec.Stop(stopCtx, time.Second)
	stopCancel()
	_ = exec.Detach()
	if err != nil {
		t.Fatalf("stop the adapter: %v", err)
	}

	if _, err := e.sched.EnsureTerminal(ctx, member); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	t.Cleanup(func() {
		_ = e.sched.StopTerminal(context.Background(), member)
		_, _ = e.sched.DestroyStoppedTerminal(context.Background(), member)
	})
	sup := e.sched.lookupLiveTerminal(member)
	terminal, err := docker.Inspect(ctx, sup.containerID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(terminal.MountTargets, coordtransport.AssistantMountDir) || slices.Contains(terminal.MountTargets, coordtransport.BinaryPath) {
		t.Fatalf("the terminal mounts the assistant's socket or binary: %v", terminal.MountTargets)
	}
}
