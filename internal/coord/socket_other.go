//go:build !linux

package coord

import (
	"context"
	"net"
)

// Non-Linux builds do not serve the production coordination Unix socket.
func watchConnection(net.Conn, context.CancelFunc, <-chan struct{}) {}
