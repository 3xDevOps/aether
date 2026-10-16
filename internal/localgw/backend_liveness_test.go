package localgw

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/testhome"
)

// A phone that slept or changed networks leaves the gateway's connection
// open and silent. The keepalive must drop it so the call waiting on it
// redials over the network that works now, instead of waiting for an answer
// that never comes.
func TestSSHBackendRedialsASilentConnection(t *testing.T) {
	shortenKeepalive(t, 100*time.Millisecond, 200*time.Millisecond)
	home := testhome.Isolate(t)
	server := listen(t)
	connections := serveControl(t, server, nil)
	path := newSilencer(t, server.Addr().String())
	b := NewSSHBackend(cli.Config{Addr: path.addr(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
		t.Fatal(perr)
	}
	path.silence()
	start := time.Now()
	if _, perr := b.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
		t.Fatalf("call on a silent connection = %v after %v, want it answered over a new one", perr, time.Since(start))
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("server accepted %d connections, want 2", got)
	}
}

// A call whose connection went silent after the request left may have
// reached the server. It fails without a replay: run.launch carries no
// request key, and a replay would start a second run.
func TestSSHBackendDoesNotReplayACallLostOnASilentConnection(t *testing.T) {
	shortenKeepalive(t, 100*time.Millisecond, 200*time.Millisecond)
	home := testhome.Isolate(t)
	server := listen(t)
	var path *silencer
	var launches atomic.Int32
	serveControl(t, server, func(method string) bool {
		if method != protocol.MethodRunLaunch {
			return true
		}
		launches.Add(1)
		path.silence()
		return false
	})
	path = newSilencer(t, server.Addr().String())
	b := NewSSHBackend(cli.Config{Addr: path.addr(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, perr := b.Call(ctx, protocol.MethodRunLaunch, nil)
	if perr == nil || perr.Code != protocol.CodeUnavailable || !strings.Contains(perr.Message, "stopped answering keepalives") {
		t.Fatalf("launch on a connection gone silent = %v, want unavailable without a replay", perr)
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("server received %d launches, want 1", got)
	}
}

// A dial in progress holds no lock: Relink and Close return at once, a
// request whose context ends stops waiting, and concurrent requests share
// the one dial instead of queueing a dial each.
func TestSSHBackendDialBlocksNothing(t *testing.T) {
	home := testhome.Isolate(t)
	ln := listen(t)
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Never speaks SSH, so the handshake waits.
			accepted.Add(1)
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	b := NewSSHBackend(cli.Config{Addr: ln.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})

	var waiting sync.WaitGroup
	for range 3 {
		waiting.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, perr := b.Call(ctx, protocol.MethodServerInfo, nil)
			if perr == nil || perr.Message != "request cancelled" {
				t.Errorf("call during a stuck dial = %v, want request cancelled", perr)
			}
		})
	}
	within(t, "calls during a stuck dial", 5*time.Second, waiting.Wait)
	within(t, "the shared dial to connect", 5*time.Second, func() {
		for accepted.Load() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	})
	if got := accepted.Load(); got != 1 {
		t.Fatalf("server accepted %d connections, want 1 shared dial", got)
	}
	within(t, "Relink during a stuck dial", time.Second, func() { b.Relink(cli.Config{Addr: closedAddr(t)}, nil) })
	within(t, "Close during a stuck dial", time.Second, func() { _ = b.Close() })
}

func within(t *testing.T, what string, limit time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s took longer than %v", what, limit)
	}
}

func shortenKeepalive(t *testing.T, interval, timeout time.Duration) {
	oldInterval, oldTimeout := keepaliveInterval, keepaliveTimeout
	keepaliveInterval, keepaliveTimeout = interval, timeout
	t.Cleanup(func() { keepaliveInterval, keepaliveTimeout = oldInterval, oldTimeout })
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// serveControl serves SSH on ln, answering keepalives and each control
// call answer allows, every call when answer is nil, with an empty result.
// It counts the connections it accepts.
func serveControl(t *testing.T, ln net.Listener, answer func(method string) bool) *atomic.Int32 {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(testhome.Ed25519Key(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	var connections atomic.Int32
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = raw.Close() }()
				conn, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				connections.Add(1)
				defer func() { _ = conn.Close() }()
				go func() {
					for req := range requests {
						_ = req.Reply(req.Type == "keepalive@openssh.com", nil)
					}
				}()
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer func() { _ = channel.Close() }()
						for request := range requests {
							_ = request.Reply(request.Type == "subsystem", nil)
							if request.Type != "subsystem" {
								continue
							}
							go ssh.DiscardRequests(requests)
							var rpc protocol.Request
							if json.NewDecoder(channel).Decode(&rpc) != nil {
								return
							}
							if answer != nil && !answer(rpc.Method) {
								return
							}
							_ = json.NewEncoder(channel).Encode(protocol.Response{JSONRPC: "2.0", ID: rpc.ID, Result: json.RawMessage(`{}`)})
							return
						}
					}()
				}
			}()
		}
	}()
	return &connections
}

// silencer relays TCP to target. silence stops every connection open so
// far from carrying anything, without closing it, as a network that went
// away does; connections opened later work.
type silencer struct {
	ln     net.Listener
	mu     sync.Mutex
	silent chan struct{}
}

func newSilencer(t *testing.T, target string) *silencer {
	t.Helper()
	s := &silencer{ln: listen(t), silent: make(chan struct{})}
	go func() {
		for {
			client, err := s.ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
			s.mu.Lock()
			silent := s.silent
			s.mu.Unlock()
			go relay(server, client, silent)
			go relay(client, server, silent)
		}
	}()
	return s
}

func (s *silencer) addr() string { return s.ln.Addr().String() }

func (s *silencer) silence() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.silent)
	s.silent = make(chan struct{})
}

func relay(dst io.Writer, src io.Reader, silent <-chan struct{}) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		select {
		case <-silent:
			return
		default:
		}
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
