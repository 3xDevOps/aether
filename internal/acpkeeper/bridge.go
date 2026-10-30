package acpkeeper

import (
	"fmt"
	"io"
	"net"
)

// Bridge connects in and out, the stdio of the exec a server attaches
// through, to the keeper at socket. It returns once the keeper closes the
// connection or out fails; in ending closes the connection's write side, so
// the keeper sees its attacher go.
func Bridge(socket string, in io.Reader, out io.Writer) error {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return fmt.Errorf("acpkeeper: dial the keeper: %w", err)
	}
	defer func() { _ = conn.Close() }()
	go func() {
		_, _ = io.Copy(conn, in)
		_ = conn.(*net.UnixConn).CloseWrite()
	}()
	if _, err := io.Copy(out, conn); err != nil {
		return fmt.Errorf("acpkeeper: relay the keeper: %w", err)
	}
	return nil
}
