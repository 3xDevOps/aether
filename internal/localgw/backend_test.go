package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/testhome"
)

// TestRoundTripCancelUnblocks: a control call stuck on a peer that never
// answers must return promptly when the caller's context is cancelled,
// as CodeUnavailable "request cancelled", by closing its channel.
func TestRoundTripCancelUnblocks(t *testing.T) {
	// A blackHolePipe accepts the request write and then never produces
	// a response byte, like a wedged TCP connection; only Close unblocks
	// the pending read.
	pipe := newBlackHolePipe()
	client := protocol.NewClient(pipe)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *protocol.Error, 1)
	go func() {
		_, err := roundTrip(ctx, client, "server.status", nil)
		var perr *protocol.Error
		if !errors.As(err, &perr) {
			t.Errorf("roundTrip error = %v, want *protocol.Error", err)
			done <- nil
			return
		}
		done <- perr
	}()

	// Let the call reach its blocked read before cancelling.
	select {
	case <-pipe.wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("request was never written")
	}
	cancel()

	select {
	case perr := <-done:
		if perr == nil {
			return // subtest already failed
		}
		if perr.Code != protocol.CodeUnavailable || perr.Message != "request cancelled" {
			t.Fatalf("error = %+v, want CodeUnavailable %q", perr, "request cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("roundTrip did not unblock on ctx cancellation")
	}
	if !pipe.closed() {
		t.Fatal("cancellation must close the control channel to unblock its read")
	}
}

// blackHolePipe is an io.ReadWriteCloser whose reads block until Close.
type blackHolePipe struct {
	wrote chan struct{} // closed after the first successful write
	done  chan struct{} // closed by Close
}

func newBlackHolePipe() *blackHolePipe {
	return &blackHolePipe{wrote: make(chan struct{}), done: make(chan struct{})}
}

func (p *blackHolePipe) Write(b []byte) (int, error) {
	select {
	case <-p.done:
		return 0, errors.New("pipe closed")
	default:
	}
	select {
	case <-p.wrote:
	default:
		close(p.wrote)
	}
	return len(b), nil
}

func (p *blackHolePipe) Read([]byte) (int, error) {
	<-p.done
	return 0, errors.New("pipe closed")
}

func (p *blackHolePipe) Close() error {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}

func (p *blackHolePipe) closed() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// TestUnreachableErrorClassification: only unambiguous local network
// failures earn the "network unreachable: " prefix; everything else stays
// "server unreachable: ". Both keep CodeUnavailable.
func TestUnreachableErrorClassification(t *testing.T) {
	opErr := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
	}
	cases := []struct {
		name   string
		err    error
		prefix string
	}{
		{"dns", &net.DNSError{Err: "no such host", Name: "aether.invalid", IsNotFound: true}, "network unreachable: "},
		{"dns wrapped", fmt.Errorf("cli: dial: %w", &net.DNSError{Err: "no such host", Name: "aether.invalid"}), "network unreachable: "},
		{"enetunreach", opErr(syscall.ENETUNREACH), "network unreachable: "},
		{"enetunreach bare wrapped", fmt.Errorf("cli: dial: %w", syscall.ENETUNREACH), "network unreachable: "},
		{"enetdown", opErr(syscall.ENETDOWN), "network unreachable: "},
		{"enetdown bare wrapped", fmt.Errorf("cli: dial: %w", syscall.ENETDOWN), "network unreachable: "},
		{"ehostunreach", opErr(syscall.EHOSTUNREACH), "network unreachable: "},
		{"ehostunreach bare wrapped", fmt.Errorf("cli: dial: %w", syscall.EHOSTUNREACH), "network unreachable: "},
		{"ehostdown", opErr(syscall.EHOSTDOWN), "network unreachable: "},
		{"ehostdown bare wrapped", fmt.Errorf("cli: dial: %w", syscall.EHOSTDOWN), "network unreachable: "},
		{"econnrefused", opErr(syscall.ECONNREFUSED), "server unreachable: "},
		{"timeout", fmt.Errorf("cli: dial: %w", os.ErrDeadlineExceeded), "server unreachable: "},
		{"handshake", errors.New("cli: ssh handshake with 127.0.0.1:1: ssh: handshake failed"), "server unreachable: "},
		{"wedged", errWedged, "server unreachable: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			perr := unreachableError(tc.err)
			if perr.Code != protocol.CodeUnavailable {
				t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeUnavailable)
			}
			if !strings.HasPrefix(perr.Message, tc.prefix) {
				t.Fatalf("message = %q, want prefix %q", perr.Message, tc.prefix)
			}
			if !strings.HasSuffix(perr.Message, tc.err.Error()) {
				t.Fatalf("message = %q, want it to carry %q", perr.Message, tc.err.Error())
			}
		})
	}
}

// closedAddr returns a loopback address guaranteed to refuse connections:
// a listener is bound to get a free port, then closed.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

// TestSSHBackendDialFailureIsCoded: a failed dial must surface as a
// *protocol.Error with CodeUnavailable on every surface, not just Call,
// so the /ws/events refusal frame carries the same code and prefix.
func TestSSHBackendDialFailureIsCoded(t *testing.T) {
	b := NewSSHBackend(cli.Config{Addr: closedAddr(t)})

	_, perr := b.Call(context.Background(), protocol.MethodServerInfo, nil)
	if perr == nil {
		t.Fatal("Call to a closed port must fail")
	}
	if perr.Code != protocol.CodeUnavailable {
		t.Fatalf("Call code = %d, want %d", perr.Code, protocol.CodeUnavailable)
	}
	if !strings.HasPrefix(perr.Message, "server unreachable: ") {
		t.Fatalf("Call message = %q, want prefix %q", perr.Message, "server unreachable: ")
	}

	_, err := b.Events(context.Background(), protocol.SubscribeRequest{})
	if err == nil {
		t.Fatal("Events against a closed port must fail")
	}
	var eperr *protocol.Error
	if !errors.As(err, &eperr) {
		t.Fatalf("Events error %v is not a *protocol.Error", err)
	}
	if eperr.Code != protocol.CodeUnavailable {
		t.Fatalf("Events code = %d, want %d", eperr.Code, protocol.CodeUnavailable)
	}
	if !strings.HasPrefix(eperr.Message, "server unreachable: ") {
		t.Fatalf("Events message = %q, want prefix %q", eperr.Message, "server unreachable: ")
	}
}

func TestSSHBackendTransportFailureRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		method    string
		wantCalls int32
		wantError bool
	}{
		{name: "config_import_no_replay", method: protocol.MethodConfigImport, wantCalls: 1, wantError: true},
		{name: "server_info_reconnects", method: protocol.MethodServerInfo, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, calls, connections := backendWithLostFirstResponse(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			result, perr := b.Call(ctx, tc.method, nil)
			if tc.wantError {
				if perr == nil || perr.Code != protocol.CodeUnavailable || result != nil {
					t.Fatalf("lost response = %s, %v; want unknown outcome without a result", result, perr)
				}
			} else if perr != nil {
				t.Fatalf("safe request did not reconnect: %v", perr)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("server received %d calls before an explicit retry, want %d", got, tc.wantCalls)
			}
			if got := connections.Load(); got != tc.wantCalls {
				t.Fatalf("server accepted %d connections before an explicit retry, want %d", got, tc.wantCalls)
			}

			// A new explicit call can recover, even for an import. Ordinary
			// calls already recovered and should reuse the healthy connection.
			if _, perr := b.Call(ctx, tc.method, nil); perr != nil {
				t.Fatalf("next explicit request did not recover: %v", perr)
			}
			if got := calls.Load(); got != tc.wantCalls+1 {
				t.Fatalf("server received %d total calls, want %d", got, tc.wantCalls+1)
			}
			if got := connections.Load(); got != 2 {
				t.Fatalf("server accepted %d total connections, want 2", got)
			}
		})
	}
}

// backendWithLostFirstResponse accepts real SSH control calls but closes the
// first request's channel without responding, after the server received it.
func backendWithLostFirstResponse(t *testing.T) (Backend, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	home := testhome.Isolate(t)
	signer, err := ssh.NewSignerFromKey(testhome.Ed25519Key(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var calls, connections atomic.Int32
	go func() {
		for {
			raw, err := listener.Accept()
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
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.UnknownChannelType, "expected session")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer func() { _ = channel.Close() }()
						for request := range requests {
							var subsystem struct{ Name string }
							ok := request.Type == "subsystem" &&
								ssh.Unmarshal(request.Payload, &subsystem) == nil &&
								subsystem.Name == protocol.SubsystemControl
							_ = request.Reply(ok, nil)
							if !ok {
								continue
							}
							go ssh.DiscardRequests(requests)
							var rpc protocol.Request
							if err := json.NewDecoder(channel).Decode(&rpc); err != nil {
								return
							}
							if calls.Add(1) == 1 {
								return
							}
							_ = json.NewEncoder(channel).Encode(protocol.Response{
								JSONRPC: "2.0", ID: rpc.ID, Result: json.RawMessage(`{}`),
							})
							return
						}
					}()
				}
			}()
		}
	}()
	b := NewSSHBackend(cli.Config{Addr: listener.Addr().String(), KnownHosts: filepath.Join(home, "known_hosts")})
	t.Cleanup(func() { _ = b.Close() })
	return b, &calls, &connections
}

// TestEventsDialFailureFrameIsUnavailable: with a real SSH backend
// pointed at a closed port, the /ws/events refusal frame must carry
// CodeUnavailable and the "server unreachable: " prefix the SPA routes
// on, not a generic internal error.
func TestEventsDialFailureFrameIsUnavailable(t *testing.T) {
	g, base := newWSGateway(t, NewSSHBackend(cli.Config{Addr: closedAddr(t)}))
	conn := wsDial(t, base, "/ws/events", g.Token())

	writeWSJSON(t, conn, protocol.SubscribeRequest{})
	ack := readWSJSON[protocol.SubscribeResponse](t, conn)
	if ack.OK {
		t.Fatalf("ack = %+v, want a refusal", ack)
	}
	if ack.Code != protocol.CodeUnavailable {
		t.Fatalf("ack code = %d, want %d", ack.Code, protocol.CodeUnavailable)
	}
	if !strings.HasPrefix(ack.Error, "server unreachable: ") {
		t.Fatalf("ack error = %q, want prefix %q", ack.Error, "server unreachable: ")
	}
}
