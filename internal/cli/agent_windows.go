//go:build windows

package cli

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
)

// openSSHAgentPipe is where Windows OpenSSH publishes ssh-agent. Unlike
// POSIX it sets no SSH_AUTH_SOCK, so the path has to be assumed.
const openSSHAgentPipe = `\\.\pipe\openssh-ssh-agent`

// platformDialAgent prefers SSH_AUTH_SOCK when a POSIX-style agent is in use
// (Go supports AF_UNIX on Windows) and otherwise reaches Windows OpenSSH over
// its named pipe. A missing pipe means no agent is running, not a failure.
func platformDialAgent(timeout time.Duration) (net.Conn, error) {
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		return (&net.Dialer{Timeout: timeout}).Dial("unix", sock)
	}
	conn, err := winio.DialPipe(openSSHAgentPipe, &timeout)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return conn, err
}
