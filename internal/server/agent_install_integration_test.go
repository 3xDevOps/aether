//go:build integration

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestIntegrationAgentInstall drives agent.install over the control channel
// against real Docker: the install command runs in the member's environment
// terminal, which the call starts, and a command that fails comes back as a
// result carrying its exit code and the end of its output, with the install
// flags read from the home. busybox has no bash or curl, so the shipped
// Claude Code installer fails the way it would on an image without them.
func TestIntegrationAgentInstall(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rt, image, _ := pickRuntime(t)
	if _, fallback := rt.(*e2eRuntime); fallback {
		t.Skip("agent.install needs a real shell in the container; Docker daemon unreachable")
	}
	srv, err := New(ctx, Config{DataDir: t.TempDir(), Addr: "127.0.0.1:0", Runtime: rt, StandardImage: image})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	_, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Installer",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#4363d8",
		Role:        domain.RoleCollaborator,
	}
	if err := srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = srv.Run(runCtx) }()
	ctrl := openControl(t, dialSSH(t, waitSSHAddr(t, srv), signer))

	var result protocol.AgentInstallResult
	if err := ctrl.Call(protocol.MethodAgentInstall, protocol.AgentInstallParams{Name: "claude", Enhanced: true}, &result); err != nil {
		t.Fatalf("agent.install: %v", err)
	}
	if result.Error != "the install command exited 127" || !strings.Contains(result.LogTail, "not found") ||
		result.Installed || result.EnhancedInstalled {
		t.Fatalf("agent.install = %+v, want the installer's own failure", result)
	}
	var status protocol.TerminalStatusResult
	if err := ctrl.Call(protocol.MethodTerminalStatus, struct{}{}, &status); err != nil || !status.Running {
		t.Fatalf("terminal.status = %+v, %v; want the environment terminal the install started", status, err)
	}
	if err := ctrl.Call(protocol.MethodTerminalStop, struct{}{}, nil); err != nil {
		t.Fatalf("terminal.stop: %v", err)
	}
}
