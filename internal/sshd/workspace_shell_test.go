package sshd

import (
	"bufio"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"golang.org/x/crypto/ssh"
)

func TestWorkspaceShellSubsystemUsesNewProtocol(t *testing.T) {
	e := newTestEnv(t, nil)
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemWorkspaceShell, func(s *ssh.Session) error {
		return s.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{})
	})
	if _, err := pipe.Write([]byte(`{"workspace":{"id":"` + string(e.member.ID) + `"},"mode":"bootstrap-tools"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	var ack protocol.WorkspaceShellResponse
	readJSONLine(t, bufio.NewReader(pipe), &ack)
	if ack.OK {
		t.Fatal("unknown workspace unexpectedly accepted")
	}
	_ = pipe.Close()
}

func TestWorkspaceShellRequestRequiresHarnessForLogin(t *testing.T) {
	req := domain.WorkspaceShellRequest{Workspace: domain.WorkspaceSelector{ID: "ws"}, Mode: domain.WorkspaceShellHarnessLogin}
	if err := req.Validate(); err == nil {
		t.Fatal("login without harness accepted")
	}
}
