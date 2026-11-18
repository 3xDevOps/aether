//go:build kvm

package vm

import (
	"bufio"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKVMKeeperStartsTheAgentFromTheHome starts an agent CLI from
// ~/.local/bin, where every agent installs, under its keeper as the
// scheduler does: a root managed exec of the staged binary that starts the
// agent as the session's user.
func TestKVMKeeperStartsTheAgentFromTheHome(t *testing.T) {
	e := kvmEngine(t)
	home := filepath.Join(e.root, "..", "src-keeper")
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	writeHome(t, home, map[string]string{".local/bin/fake-agent": "#!/bin/sh\necho \"$(id -u) $PATH\" > \"$HOME/agent-ran\"\nexec sleep infinity\n"})
	if err := os.Chmod(filepath.Join(home, ".local", "bin", "fake-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	kvmUserSession(t, e, "run-keeper", runtime.Mount{HostPath: home, ContainerPath: "/root"},
		runtime.Mount{HostPath: e.cfg.Helper, ContainerPath: coordtransport.CLIPath, ReadOnly: true, Kind: runtime.MountServerBinary})
	m, err := e.startManagedExec(t.Context(), "run-keeper", runtime.ExecSpec{
		Argv: acpkeeper.ServeArgv(coordtransport.CLIPath, path.Join(acpkeeper.SocketRoot, "test", "keeper.sock"), sessionUser, []string{"fake-agent"}),
		User: "0", CreationKey: "acp-test",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.Stop(context.Background(), 0)
		_ = m.Detach()
	})
	att := m.Attachment()
	if _, err = io.WriteString(att.Stdin(), "token\n"); err != nil {
		t.Fatal(err)
	}
	if line, _ := bufio.NewReader(att.Stdout()).ReadString('\n'); line != "ready\n" {
		stderr, _ := io.ReadAll(att.Stderr())
		t.Fatalf("the keeper answered %q: %s", line, stderr)
	}
	want := "1000 /root/.local/bin:"
	for start := time.Now(); ; time.Sleep(100 * time.Millisecond) {
		ran, _ := os.ReadFile(filepath.Join(home, "agent-ran"))
		if strings.HasPrefix(string(ran), want) {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("the agent wrote %q, want %q...", ran, want)
		}
	}
}
