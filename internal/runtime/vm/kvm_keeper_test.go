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

// TestKVMShutdownLetsGoOfAFrozenVMsKeeper lets go of an agent's keeper as a
// server shutdown does, in a VM the memory brake froze: the detach waits 3
// seconds for an answer that never comes, then the close ends the attachment
// without waiting for the guest to take the detach. It closes the client a
// second time, as a shutdown closes a terminal's, which must not wait again.
func TestKVMShutdownLetsGoOfAFrozenVMsKeeper(t *testing.T) {
	e := kvmEngine(t)
	const key = "run-keeper-frozen"
	kvmUserSession(t, e, key, runtime.Mount{HostPath: e.cfg.Helper, ContainerPath: coordtransport.CLIPath, ReadOnly: true, Kind: runtime.MountServerBinary})
	socket := path.Join(acpkeeper.SocketRoot, "frozen", "keeper.sock")
	keeper, err := e.startManagedExec(t.Context(), key, runtime.ExecSpec{
		Argv: acpkeeper.ServeArgv(coordtransport.CLIPath, socket, sessionUser, []string{"sleep", "infinity"}),
		User: "0", CreationKey: "acp-frozen",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(keeper.Attachment().Stdin(), "token\n"); err != nil {
		t.Fatal(err)
	}
	if line, _ := bufio.NewReader(keeper.Attachment().Stdout()).ReadString('\n'); line != "ready\n" {
		t.Fatalf("the keeper answered %q", line)
	}
	_ = keeper.Detach()
	bridge, err := e.startManagedExec(t.Context(), key, runtime.ExecSpec{
		Argv: acpkeeper.AttachArgv(coordtransport.CLIPath, socket), User: "0", CreationKey: "acp-attach-frozen",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	client, err := acpkeeper.Attach(t.Context(), execConn{bridge.Attachment()}, acpkeeper.Hello{Gen: 1, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	froze, err := e.freeze(key)
	if !froze || err != nil {
		t.Fatalf("freeze = %v, %v", froze, err)
	}
	t.Cleanup(func() { _ = e.thaw(key) })

	start := time.Now()
	detached := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		detachErr := client.Detach(ctx)
		_ = client.Close()
		_ = client.Close()
		_ = bridge.Detach()
		detached <- detachErr
	}()
	select {
	case err = <-detached:
		if err == nil {
			t.Fatal("the frozen guest confirmed the detach")
		}
	case <-time.After(12 * time.Second):
		t.Fatal("letting go of the frozen VM's keeper still waits after 12 seconds")
	}
	t.Logf("let go of the frozen VM's keeper in %s", time.Since(start).Round(time.Millisecond))
}

// execConn is an exec's stdio as one stream, as the scheduler's bridge to a
// keeper is.
type execConn struct{ att runtime.Attachment }

func (c execConn) Read(p []byte) (int, error)  { return c.att.Stdout().Read(p) }
func (c execConn) Write(p []byte) (int, error) { return c.att.Stdin().Write(p) }
func (c execConn) Close() error                { return c.att.Stdin().Close() }
