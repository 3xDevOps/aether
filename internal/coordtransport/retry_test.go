package coordtransport

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "act-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "coord.sock")
}

// serveEach answers every connection with handle until the returned stop
// is called. Stopping closes the listener but leaves the socket file, as a
// stopped server does.
func serveEach(t *testing.T, path string, handle func(net.Conn, string)) (stop func()) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				line, err := protocol.ReadLine(bufio.NewReader(conn))
				if err != nil {
					return
				}
				handle(conn, string(line))
			}()
		}
	}()
	return func() { _ = ln.Close() }
}

const okLine = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n"

func TestCallRetriesDialAcrossRestart(t *testing.T) {
	path := socketPath(t)
	stop := serveEach(t, path, func(c net.Conn, _ string) { _, _ = c.Write([]byte(okLine)) })
	stop() // socket file remains: ECONNREFUSED
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = os.Remove(path) // ENOENT for a moment, then rebound
		time.Sleep(300 * time.Millisecond)
		serveEach(t, path, func(c net.Conn, _ string) { _, _ = c.Write([]byte(okLine)) })
	}()
	// Unkeyed and mutating, yet safe to retry: nothing was ever delivered.
	err := Call(context.Background(), path, protocol.MethodDevTerminalInput, protocol.DevTerminalInputParams{Kind: "text", Text: "x"}, nil)
	if err != nil {
		t.Fatalf("call across restart: %v", err)
	}
}

func TestCallRetriesServerRunClosing(t *testing.T) {
	path := socketPath(t)
	var n atomic.Int32
	serveEach(t, path, func(c net.Conn, _ string) {
		if n.Add(1) == 1 {
			_, _ = c.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32004,\"message\":\"coord.send: run is closing\"}}\n"))
			return
		}
		_, _ = c.Write([]byte(okLine))
	})
	if err := Call(context.Background(), path, protocol.MethodCoordSend, protocol.CoordSendParams{Body: "x"}, nil); err != nil {
		t.Fatalf("call: %v", err)
	}
}

// cutAfterRead drops the first connection after reading the request, which
// is the server dying mid-call.
func cutAfterRead(t *testing.T, path string) *atomic.Int32 {
	var n atomic.Int32
	serveEach(t, path, func(c net.Conn, _ string) {
		if n.Add(1) == 1 {
			return
		}
		_, _ = c.Write([]byte(okLine))
	})
	return &n
}

func TestCallRetriesSentRequestOnlyWhenRepeatable(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		params       any
		wantRetry    bool
	}{
		{"keyed send", protocol.MethodCoordSend, protocol.CoordSendParams{Body: "x", IdempotencyKey: "k1"}, true},
		{"inbox read", protocol.MethodCoordInbox, protocol.CoordInboxParams{}, true},
		{"read-only terminal output", protocol.MethodDevTerminalOutput, protocol.DevTerminalOutputParams{}, true},
		{"unkeyed send", protocol.MethodCoordSend, protocol.CoordSendParams{Body: "x"}, false},
		{"terminal input", protocol.MethodDevTerminalInput, protocol.DevTerminalInputParams{Kind: "text", Text: "rm -rf x\n"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := socketPath(t)
			n := cutAfterRead(t, path)
			err := Call(context.Background(), path, tc.method, tc.params, nil)
			if tc.wantRetry {
				if err != nil || n.Load() != 2 {
					t.Fatalf("err=%v attempts=%d; want a retry that succeeds", err, n.Load())
				}
				return
			}
			if ErrorCode(err) != protocol.CodeUnavailable || !strings.Contains(err.Error(), "may already have applied") || n.Load() != 1 {
				t.Fatalf("err=%v attempts=%d; want one attempt and an ambiguity message", err, n.Load())
			}
		})
	}
}

func TestCallReusesOneKeyAcrossAttempts(t *testing.T) {
	path := socketPath(t)
	var mu sync.Mutex
	var keys []string
	var n atomic.Int32
	serveEach(t, path, func(c net.Conn, line string) {
		mu.Lock()
		keys = append(keys, line)
		mu.Unlock()
		if n.Add(1) == 1 {
			return
		}
		_, _ = c.Write([]byte(okLine))
	})
	if err := Call(context.Background(), path, protocol.MethodCoordSend, protocol.CoordSendParams{Body: "x", IdempotencyKey: "same"}, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("attempts sent %q, want identical requests", keys)
	}
}

func TestCallDoesNotRetryServerErrors(t *testing.T) {
	path := socketPath(t)
	var n atomic.Int32
	serveEach(t, path, func(c net.Conn, _ string) {
		n.Add(1)
		_, _ = c.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32001,\"message\":\"denied\"}}\n"))
	})
	err := Call(context.Background(), path, protocol.MethodCoordStatus, nil, nil)
	if ErrorCode(err) != -32001 || n.Load() != 1 {
		t.Fatalf("err=%v attempts=%d; want the server's error once", err, n.Load())
	}
}

func TestCallStopsRetryingWithContext(t *testing.T) {
	path := socketPath(t) // never bound
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	err := Call(ctx, path, protocol.MethodCoordSend, protocol.CoordSendParams{Body: "x", IdempotencyKey: "k"}, nil)
	if ErrorCode(err) != protocol.CodeUnavailable || !strings.Contains(err.Error(), "not delivered and nothing applied") {
		t.Fatalf("err=%v; want unavailable stating nothing applied", err)
	}
}
