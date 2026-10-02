package coordtransport

import (
	"bufio"
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestWakeCallCancellationClosesSocket(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			requestRead := make(chan struct{})
			peerDone := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					peerDone <- err
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				reader := bufio.NewReader(conn)
				if _, readErr := protocol.ReadLine(reader); readErr != nil {
					peerDone <- readErr
					return
				}
				close(requestRead)
				_, err = reader.ReadByte()
				peerDone <- err
			}()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if deadline {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 200*time.Millisecond)
				defer stop()
			}
			callDone := make(chan error, 1)
			go func() {
				callDone <- Call(ctx, path, protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{WaitSeconds: 30}, nil)
			}()
			select {
			case <-requestRead:
			case err := <-peerDone:
				t.Fatalf("peer failed before reading request: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("wake request never reached socket peer")
			}
			if !deadline {
				cancel()
			}
			select {
			case err := <-callDone:
				if ErrorCode(err) != protocol.CodeUnavailable {
					t.Fatalf("cancelled wake = %v; want unavailable", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("wake call ignored context termination")
			}
			select {
			case err := <-peerDone:
				if err != io.EOF {
					t.Fatalf("peer observed %v, want closed connection", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled wake left a server connection open")
			}
		})
	}
}
