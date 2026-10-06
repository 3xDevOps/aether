package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// ErrAgentInstallRunning refuses a second install for a member while one
// runs: two npm installs into one ~/.local race each other.
var ErrAgentInstallRunning = errors.New("agent install: another install is running in this environment; wait for it to finish")

// maxInstallLog is how much of the end of an install's output is kept.
const maxInstallLog = 8 << 10

// InstallAgent runs command, an agent's install command, with /bin/sh in
// the member's environment terminal, starting the terminal when it is not
// running, and returns the end of its combined output and its exit code.
// The command installs into the member home, which the terminal mounts,
// so nothing has to be saved for runs to find it.
func (s *Scheduler) InstallAgent(ctx context.Context, member domain.MemberID, command string) (string, int, error) {
	s.mu.Lock()
	if s.agentInstalls[member] {
		s.mu.Unlock()
		return "", 0, ErrAgentInstallRunning
	}
	if s.agentInstalls == nil {
		s.agentInstalls = make(map[domain.MemberID]bool)
	}
	s.agentInstalls[member] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.agentInstalls, member)
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, protocol.AgentInstallTimeout)
	defer cancel()
	if _, err := s.EnsureTerminal(ctx, member); err != nil {
		return "", 0, err
	}
	sup := s.lookupLiveTerminal(member)
	if sup == nil {
		return "", 0, ErrTerminalNotRunning
	}
	// Exec refuses a command whose output passes 1 MiB, and an installer's
	// progress output can, so only the end of the log comes back.
	script := fmt.Sprintf("log=$(mktemp) || exit 1\n(\n%s\n) >\"$log\" 2>&1\ncode=$?\ntail -c %d \"$log\"\nrm -f \"$log\"\nexit $code", command, maxInstallLog)
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, sup.containerID, []string{"/bin/sh", "-c", script}, sup.home)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, fmt.Errorf("agent install: did not finish within %s", protocol.AgentInstallTimeout)
		}
		return "", 0, fmt.Errorf("agent install: run in the environment terminal: %w", err)
	}
	return logTail(joinOutput(stdout, stderr)), code, nil
}

// logTail drops the partial first line a byte-count tail leaves, and the
// invalid UTF-8 a cut through a character does.
func logTail(out string) string {
	if len(out) >= maxInstallLog {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return strings.ToValidUTF8(out, "")
}
