//go:build integration

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TestIntegrationTerminalExec runs the linked CLI against the wired server.
// A viewer is enough: the environment terminal needs no capability. The
// in-process runtime has no programs to run, so there the streams and status
// that must pass through are its own refusal.
func TestIntegrationTerminalExec(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rt, image, _ := pickRuntime(t)
	srv, err := New(ctx, Config{DataDir: t.TempDir(), Addr: "127.0.0.1:0", Runtime: rt, StandardImage: image})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	keyPath, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Viewer",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#4363d8",
		Role:        domain.RoleViewer,
	}
	if err := srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = srv.Run(runCtx) }()
	aether := buildCLIBinary(t)
	viewer := newSSHUser(t, aether, waitSSHAddr(t, srv), keyPath)

	// The first call also records the server's host key, on stderr.
	if stdout, stderr, code := viewer.run("", aether, "terminal", "status"); stdout != "status\tstopped\n" || code != 0 {
		t.Fatalf("terminal status before exec = stdout %q stderr %q status %d, want a stopped environment", stdout, stderr, code)
	}

	wantOut, wantErr, wantCode := "/root\n/root\n", "oops\n", 7
	if _, fallback := rt.(*e2eRuntime); fallback {
		wantOut, wantErr, wantCode = "", "exec: not supported by the e2e runtime", 127
	}
	stdout, stderr, code := viewer.run("", aether, "terminal", "exec", `echo "$HOME";`, "pwd; echo oops >&2; exit 7")
	if stdout != wantOut || stderr != wantErr || code != wantCode {
		t.Fatalf("terminal exec = stdout %q stderr %q status %d, want %q, %q and %d", stdout, stderr, code, wantOut, wantErr, wantCode)
	}

	if stdout, stderr, code = viewer.run("", aether, "terminal", "status"); !strings.HasPrefix(stdout, "status\trunning\n") || code != 0 {
		t.Fatalf("terminal status after exec = stdout %q stderr %q status %d, want the environment exec started", stdout, stderr, code)
	}
	if _, stderr, code = viewer.run("", aether, "terminal", "exec"); code != 1 || !strings.Contains(stderr, "usage: aether terminal") {
		t.Fatalf("terminal exec without a command = stderr %q status %d, want the usage error", stderr, code)
	}
	if _, stderr, code = viewer.run("", aether, "terminal", "stop"); code != 0 {
		t.Fatalf("terminal stop exited %d: %s", code, stderr)
	}
}
