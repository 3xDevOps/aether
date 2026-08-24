//go:build !windows

package cli

import (
	"net"
	"os"
	"time"
)

// platformDialAgent reaches the agent over the unix socket advertised by
// SSH_AUTH_SOCK. An unset variable means no agent is configured.
func platformDialAgent(timeout time.Duration) (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil
	}
	return (&net.Dialer{Timeout: timeout}).Dial("unix", sock)
}
