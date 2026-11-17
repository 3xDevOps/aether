package helper

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/guest"
)

// listenLoopbacks listens on each loopback port and relays every connection
// to its socket in the VM.
func listenLoopbacks(loopbacks []guest.Loopback) error {
	var errs []error
	for _, l := range loopbacks {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(l.Port))))
		if err != nil {
			errs = append(errs, fmt.Errorf("loopback %d: %w", l.Port, err))
			continue
		}
		go serveLoopback(listener, l.Path)
	}
	return errors.Join(errs...)
}

func serveLoopback(listener net.Listener, path string) {
	for {
		conn, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			slog.Warn("guest: accept on a loopback port", "addr", listener.Addr(), "error", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go relayLoopback(conn, path)
	}
}

func relayLoopback(conn net.Conn, path string) {
	defer func() { _ = conn.Close() }()
	target, err := net.Dial("unix", path)
	if err != nil {
		slog.Warn("guest: reach a loopback port's socket", "path", path, "error", err)
		return
	}
	defer func() { _ = target.Close() }()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); splice(target, conn) }()
	go func() { defer wg.Done(); splice(conn, target) }()
	wg.Wait()
}
