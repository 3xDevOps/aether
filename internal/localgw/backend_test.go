package localgw

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
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

	_, err := b.Events(protocol.SubscribeRequest{})
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
