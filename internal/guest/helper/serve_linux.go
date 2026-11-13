package helper

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/mdlayher/vsock"
)

// serveSockets listens on each served socket's vsock port for the host and
// relays each connection to its socket in the VM. The helper writes one zero
// byte once that socket accepted, then relays; a socket that does not accept
// gets the connection closed, so the host can tell one that is up.
func serveSockets(serves []guest.Socket) error {
	var errs []error
	for _, s := range serves {
		l, err := vsock.Listen(s.Port, nil)
		if err != nil {
			errs = append(errs, fmt.Errorf("serve %s on vsock port %d: %w", s.Path, s.Port, err))
			continue
		}
		go serveSocket(hostOnly{l}, s.Path)
	}
	return errors.Join(errs...)
}

func serveSocket(l net.Listener, path string) {
	for {
		conn, err := l.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			slog.Warn("guest: accept on a served socket", "path", path, "error", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go relayIn(conn, path)
	}
}

func relayIn(host net.Conn, path string) {
	defer func() { _ = host.Close() }()
	conn, err := net.Dial("unix", path)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	if _, err = host.Write([]byte{0}); err != nil {
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); splice(host, conn) }()
	go func() { defer wg.Done(); splice(conn, host) }()
	wg.Wait()
}
