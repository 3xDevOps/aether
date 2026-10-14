package scheduler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// terminalExecScript runs $1 in the login shell a terminal tab starts and
// stops it at the time limit, so a command whose call was dropped does not
// run on past it. An inner shell starts the command in its own process
// group, where setsid exists, and records its exit status in a file. At the
// limit timeout signals that shell, which kills the whole group, children
// that ignore TERM included, and records nothing; only then does the script
// end stderr with $2. A command that ends by itself, with any status, is
// never reported as stopped.
const terminalExecScript = `if [ -x /bin/bash ]; then shell=/bin/bash; else shell=/bin/sh; fi
status=$(mktemp) || exit 1
timeout %d /bin/sh -c 'if command -v setsid >/dev/null 2>&1; then setsid "$0" -lc "$1" & else "$0" -lc "$1" & fi
command=$!
trap "kill -s KILL -- -$command $command 2>/dev/null; exit 143" TERM
wait "$command"
echo $? >"$2"' "$shell" "$1" "$status"
stopped=$?
code=$(cat "$status")
rm -f "$status"
if [ -n "$code" ]; then exit "$code"; fi
if [ "$stopped" -eq 124 ] || [ "$stopped" -eq 143 ]; then echo "$2" >&2; fi
exit "$stopped"`

// terminalExecArgv is the exec for command. mark is what the script ends
// stderr with when limit stopped the command; no command can guess it.
func terminalExecArgv(command, mark string, limit time.Duration) []string {
	return []string{"/bin/sh", "-c", fmt.Sprintf(terminalExecScript, int(limit.Seconds())), "sh", command, mark}
}

// ExecTerminal runs command in the member's environment terminal, starting it
// if needed. The terminal lock is not held across the exec, so opening a tab
// never waits on a command.
func (s *Scheduler) ExecTerminal(ctx context.Context, member domain.MemberID, command string) (code int, stdout, stderr string, err error) {
	if _, err = s.EnsureTerminal(ctx, member); err != nil {
		return 0, "", "", err
	}
	sup := s.lookupLiveTerminal(member)
	if sup == nil {
		return 0, "", "", ErrTerminalNotRunning
	}
	ctx, cancel := context.WithTimeout(ctx, protocol.TerminalExecTimeout+execKillGrace)
	defer cancel()
	mark := rand.Text()
	code, stdout, stderr, err = s.cfg.Runtime.Exec(ctx, sup.containerID, terminalExecArgv(command, mark, protocol.TerminalExecTimeout), sup.home)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (err == nil && strings.HasSuffix(stderr, mark+"\n")) {
		return 0, "", "", fmt.Errorf("terminal exec: the command passed the %s time limit", protocol.TerminalExecTimeout)
	}
	if err != nil {
		return 0, "", "", fmt.Errorf("terminal exec: %w", err)
	}
	return code, stdout, stderr, nil
}
