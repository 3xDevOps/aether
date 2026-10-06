package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// ErrAgentInstallRunning refuses a second install for a member while one
// runs: two npm installs into one ~/.local race each other.
var ErrAgentInstallRunning = errors.New("agent install: another install is running in this environment; wait for it to finish")

const maxInstallLog = 8 << 10

// installExecGrace is how long past AgentInstallTimeout the exec may take
// to answer once the container has killed the installer.
const installExecGrace = 30 * time.Second

// InstallAgent runs an agent's install command in the member's environment
// terminal, starting it if needed. The terminal lock is not held across the
// exec, so opening a tab never waits on an installer.
func (s *Scheduler) InstallAgent(ctx context.Context, member domain.MemberID, command string) (string, int, error) {
	homePath, err := s.cfg.Homes.Path(member)
	if err != nil {
		return "", 0, fmt.Errorf("agent install: resolve the member home: %w", err)
	}
	if err = s.holdHomeForInstall(ctx, member, homePath); err != nil {
		return "", 0, err
	}
	defer func() {
		s.mu.Lock()
		delete(s.agentInstalls, member)
		s.mu.Unlock()
	}()

	if _, err = s.EnsureTerminal(ctx, member); err != nil {
		return "", 0, err
	}
	s.mu.Lock()
	sup := s.terminals[member]
	if sup == nil || sup.cleanupPending {
		s.mu.Unlock()
		return "", 0, ErrTerminalNotRunning
	}
	containerID, home := sup.containerID, sup.home
	s.mu.Unlock()

	// The container kills the install at AgentInstallTimeout, so the guard is
	// never released while an installer still writes into the home. Exec
	// refuses output past 1 MiB, so only the log's tail comes back.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), protocol.AgentInstallTimeout+installExecGrace)
	defer cancel()
	script := fmt.Sprintf("log=$(mktemp) || exit 1\ntimeout -s KILL %d sh -c \"$1\" >\"$log\" 2>&1\ncode=$?\ntail -c %d \"$log\"\nrm -f \"$log\"\nexit $code",
		int(protocol.AgentInstallTimeout.Seconds()), maxInstallLog)
	start := time.Now()
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, containerID, []string{"/bin/sh", "-c", script, "sh", command}, home)
	if err != nil {
		return "", 0, fmt.Errorf("agent install: run in the environment terminal: %w", err)
	}
	tail := joinOutput(logTail(stdout), stderr)
	if code != 0 && time.Since(start) >= protocol.AgentInstallTimeout {
		return "", 0, fmt.Errorf("agent install: did not finish within %s; the end of its output:\n%s", protocol.AgentInstallTimeout, tail)
	}
	return tail, code, nil
}

// holdHomeForInstall claims homePath once no harness update runs in it;
// updateHarness starts no update into a claimed home.
func (s *Scheduler) holdHomeForInstall(ctx context.Context, member domain.MemberID, homePath string) error {
	for {
		s.mu.Lock()
		if _, running := s.agentInstalls[member]; running {
			s.mu.Unlock()
			return ErrAgentInstallRunning
		}
		var update *harnessUpdateRun
		for key, state := range s.harnessUpdates {
			if key.home == homePath && state.running != nil {
				update = state.running
				break
			}
		}
		if update == nil {
			if s.agentInstalls == nil {
				s.agentInstalls = make(map[domain.MemberID]string)
			}
			s.agentInstalls[member] = homePath
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		select {
		case <-update.done:
		case <-ctx.Done():
			return fmt.Errorf("agent install: wait for the harness update in this environment: %w", ctx.Err())
		}
	}
}

// logTail drops the partial first line a byte-count tail of the install log
// leaves, and the invalid UTF-8 a cut through a character does.
func logTail(out string) string {
	if len(out) >= maxInstallLog {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return strings.ToValidUTF8(out, "")
}
