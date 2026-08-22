package localgw

import (
	"context"
	"errors"
	"testing"
	"time"

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
