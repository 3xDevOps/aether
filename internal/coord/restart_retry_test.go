package coord

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// Calls issued while the service is down (listeners closed, socket file
// still on disk, as in a stopped server) must complete once the next
// service rebinds, without failing or applying twice.
func TestCallsSurviveServiceRestart(t *testing.T) {
	h := newHarness(t, 2)
	h.start()
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	dir, err := h.svc.Provision(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, coordtransport.SocketName)
	if err = h.svc.Close(); err != nil {
		t.Fatal(err)
	}

	send := sendParams(b, "sent during the restart")
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	call := func(method string, params, result any) {
		defer wg.Done()
		errs <- coordtransport.Call(ctx, socket, method, params, result)
	}
	var status protocol.CoordStatusResult
	var sent protocol.CoordSendResult
	var inbox protocol.CoordInboxResult
	wg.Add(3)
	go call(protocol.MethodCoordStatus, nil, &status)
	go call(protocol.MethodCoordSend, send, &sent)
	go call(protocol.MethodCoordInbox, protocol.CoordInboxParams{}, &inbox)
	time.Sleep(400 * time.Millisecond)
	h.restart(t, false)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("call across the restart: %v", err)
		}
	}
	n, err := h.db.CountUnackedRunMessages(ctx, b)
	if err != nil || n != 1 {
		t.Fatalf("unacked for recipient = %d, %v; want exactly one", n, err)
	}
}

// A retry with the same idempotency key after the store is reopened, as
// after a server restart, adds no second message and returns the first
// receipt.
func TestSendDedupeIsDurableAcrossStoreReopen(t *testing.T) {
	h := newHarness(t, 2)
	h.start()
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	p := sendParams(b, "once")
	first, rpcErr := h.svc.Send(ctx, a, p)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if err := h.svc.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.dir, "aether.db")
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := storetest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.db = db
	h.restart(t, false)

	again, rpcErr := h.svc.Send(ctx, a, p)
	if rpcErr != nil {
		t.Fatalf("retry after reopen: %v", rpcErr)
	}
	if again.MessageID != first.MessageID {
		t.Fatalf("retry receipt = %+v, want the first %+v", again, first)
	}
	if n, err := db.CountUnackedRunMessages(ctx, b); err != nil || n != 1 {
		t.Fatalf("unacked after the retry = %d, %v; want 1", n, err)
	}

	batch, rpcErr := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{})
	if rpcErr != nil || len(batch.Messages) != 1 {
		t.Fatalf("inbox = %+v, %v", batch, rpcErr)
	}
	// The cut connection loses nothing: the same batch returns under the
	// same token, also across another reopen.
	again2, rpcErr := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{})
	if rpcErr != nil || again2.AckToken != batch.AckToken || len(again2.Messages) != 1 {
		t.Fatalf("redelivery = %+v, %v; want the same batch", again2, rpcErr)
	}
}
