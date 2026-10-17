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
	path := newSilencer(t, server.Addr().String(), 0)
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
	if elapsed := time.Since(start); elapsed > (keepaliveCount+1)*keepaliveInterval+time.Second {
		t.Fatalf("silent connection dropped after %v, want within %d keepalive intervals", elapsed, keepaliveCount+1)
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("server accepted %d connections, want 2", got)
	}
}

// A weak mobile link queues the keepalive reply behind the data already in
// flight. A transfer that keeps the connection busy for many keepalive
// intervals must survive: its bytes prove the connection alive.
func TestSSHBackendKeepsASlowBusyConnection(t *testing.T) {
	shortenKeepalive(t, 100*time.Millisecond, 200*time.Millisecond)
	home := testhome.Isolate(t)
	server := listen(t)
	payload := strings.Repeat("a", 1<<20)
	connections := serveControl(t, server, func(method string) json.RawMessage {
		if method != protocol.MethodHomeDownload {
			return json.RawMessage(`{}`)
		}
		return json.RawMessage(`{"data":"` + payload + `"}`)
	})
	path := newSilencer(t, server.Addr().String(), 12*time.Millisecond)
	b := NewSSHBackend(cli.Config{Addr: path.addr(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	result, perr := b.Call(ctx, protocol.MethodHomeDownload, nil)
	if perr != nil {
		t.Fatalf("download over a slow link = %v after %v, want it delivered", perr, time.Since(start))
	}
	if len(result) < len(payload) {
		t.Fatalf("download returned %d bytes, want at least %d", len(result), len(payload))
	}
	if elapsed := time.Since(start); elapsed < 10*keepaliveInterval {
		t.Fatalf("download took %v, want the link slow enough to span many keepalive intervals", elapsed)
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("server accepted %d connections, want the first kept", got)
	}
}

// A call whose connection went silent after the request left may have
// reached the server. One unsafe to repeat fails without a replay, since a
// replayed run.launch starts a second run; a read is answered over a new
// connection.
func TestSSHBackendReplaysOnlySafeCallsLostOnASilentConnection(t *testing.T) {
	for _, tc := range []struct {
		method    string
		wantCalls int32
	}{
		{method: protocol.MethodRunLaunch, wantCalls: 1},
		{method: protocol.MethodRunList, wantCalls: 2},
	} {
		t.Run(tc.method, func(t *testing.T) {
			shortenKeepalive(t, 100*time.Millisecond, 200*time.Millisecond)
			home := testhome.Isolate(t)
			server := listen(t)
			var path *silencer
			var calls atomic.Int32
			serveControl(t, server, func(method string) json.RawMessage {
				if method != tc.method {
					return json.RawMessage(`{}`)
				}
				if calls.Add(1) > 1 {
					return json.RawMessage(`{}`)
				}
				path.silence()
				return nil
			})
			path = newSilencer(t, server.Addr().String(), 0)
			b := NewSSHBackend(cli.Config{Addr: path.addr(), KnownHosts: filepath.Join(home, "known_hosts")})
			t.Cleanup(func() { _ = b.Close() })

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, perr := b.Call(ctx, tc.method, nil)
			if tc.wantCalls == 1 {
				if perr == nil || perr.Code != protocol.CodeUnavailable || !strings.Contains(perr.Message, "stopped answering keepalives") {
					t.Fatalf("%s on a connection gone silent = %v, want unavailable without a replay", tc.method, perr)
				}
			} else if perr != nil {
				t.Fatalf("%s on a connection gone silent = %v, want it answered over a new one", tc.method, perr)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("server received %d %s calls, want %d", got, tc.method, tc.wantCalls)
			}
		})
	}
}

// A server handler that does not answer within the call's bound fails that
// call alone. The connection stays up, so a call already in flight on it is
// answered without a replay and nothing redials.
func TestSSHBackendSlowCallFailsAlone(t *testing.T) {
	oldTimeout := callTimeout
	callTimeout = 500 * time.Millisecond
	t.Cleanup(func() { callTimeout = oldTimeout })
	home := testhome.Isolate(t)
	server := listen(t)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseAll)
	var disks, downloads atomic.Int32
	connections := serveControl(t, server, func(method string) json.RawMessage {
		switch method {
		case protocol.MethodServerDisk:
			disks.Add(1)
		case protocol.MethodHomeDownload:
			downloads.Add(1)
		default:
			return json.RawMessage(`{}`)
		}
		started <- struct{}{}
		<-release
		if method == protocol.MethodServerDisk {
			return nil
		}
		return json.RawMessage(`{}`)
	})
	b := NewSSHBackend(cli.Config{Addr: server.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	disk := make(chan *protocol.Error, 1)
	download := make(chan *protocol.Error, 1)
	go func() {
		_, perr := b.Call(ctx, protocol.MethodServerDisk, nil)
		disk <- perr
	}()
	go func() {
		_, perr := b.Call(ctx, protocol.MethodHomeDownload, nil)
		download <- perr
	}()
	for range 2 {
		select {
		case <-started:
		case perr := <-disk:
			t.Fatalf("server.disk ended before its handler started: %v", perr)
		case perr := <-download:
			t.Fatalf("home.download ended before its handler started: %v", perr)
		case <-ctx.Done():
			t.Fatal("the handlers did not start within 10s")
		}
	}

	perr := <-disk
	releaseAll()
	if want := "server did not answer server.disk within 500ms"; perr == nil || perr.Code != protocol.CodeUnavailable || perr.Message != want {
		t.Fatalf("server.disk past its bound = %v, want unavailable %q", perr, want)
	}
	if perr := <-download; perr != nil {
		t.Fatalf("home.download in flight beside the slow call = %v, want it answered", perr)
	}
	if got := disks.Load(); got != 1 {
		t.Fatalf("server received %d server.disk calls, want 1", got)
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("server received %d home.download calls, want 1", got)
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("server accepted %d connections, want 1", got)
	}
}

// A config reload relinks to the same server while a request waits on a
// dial; the request follows the new config instead of failing.
func TestSSHBackendRequestFollowsARelinkDuringADial(t *testing.T) {
	home := testhome.Isolate(t)
	stuck := listen(t)
	go func() {
		for {
			conn, err := stuck.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	server := listen(t)
	serveControl(t, server, nil)
	knownHosts := filepath.Join(home, "known_hosts")
	b := NewSSHBackend(cli.Config{Addr: stuck.Addr().String(), KnownHosts: knownHosts})
	t.Cleanup(func() { _ = b.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	answered := make(chan *protocol.Error, 1)
	go func() {
		_, perr := b.Call(ctx, protocol.MethodServerInfo, nil)
		answered <- perr
	}()
	time.Sleep(100 * time.Millisecond)
	b.Relink(cli.Config{Addr: server.Addr().String(), KnownHosts: knownHosts}, nil)
	if perr := <-answered; perr != nil {
		t.Fatalf("call waiting on a dial a relink abandoned = %v, want it answered over the new config", perr)
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
// call with the result answer gives, none for a nil one; a nil answer
// gives every call an empty result. It counts the connections it accepts.
func serveControl(t *testing.T, ln net.Listener, answer func(method string) json.RawMessage) *atomic.Int32 {
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
							result := json.RawMessage(`{}`)
							if answer != nil {
								result = answer(rpc.Method)
							}
							if result == nil {
								return
							}
							_ = json.NewEncoder(channel).Encode(protocol.Response{JSONRPC: "2.0", ID: rpc.ID, Result: result})
							return
						}
					}()
				}
			}()
		}
	}()
	return &connections
}

// silencer relays TCP to target, pausing pace after each 4 KiB toward the
// client when pace is set, as a weak mobile link does. silence stops every
// connection open so far from carrying anything, without closing it, as a
// network that went away does; connections opened later work.
type silencer struct {
	ln     net.Listener
	mu     sync.Mutex
	silent chan struct{}
}

func newSilencer(t *testing.T, target string, pace time.Duration) *silencer {
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
			go relay(server, client, silent, 0)
			go relay(client, server, silent, pace)
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

func relay(dst io.Writer, src io.Reader, silent <-chan struct{}, pace time.Duration) {
	buf := make([]byte, 32<<10)
	if pace > 0 {
		buf = buf[:4<<10]
	}
	for {
		time.Sleep(pace)
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
