package events

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

var _ Bus = (*InProc)(nil)

const testTimeout = 10 * time.Second

func newTestBus(t *testing.T, log EventLog) *InProc {
	t.Helper()
	b, err := NewInProc(context.Background(), log)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func publish(t *testing.T, b *InProc, e Event) Event {
	t.Helper()
	out, err := b.Publish(context.Background(), e)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return out
}

func statusEvent(session domain.SessionID, run domain.RunID, to domain.RunStatus) Event {
	return Event{
		SessionID: session,
		RunID:     run,
		ActorID:   "m1",
		Payload:   RunStatusPayload{To: to},
	}
}

// collect receives n events from sub or fails the test on timeout.
func collect(t *testing.T, sub Subscription, n int) []Event {
	t.Helper()
	out := make([]Event, 0, n)
	deadline := time.After(testTimeout)
	for len(out) < n {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				t.Fatalf("events channel closed after %d of %d events", len(out), n)
			}
			out = append(out, e)
		case <-deadline:
			t.Fatalf("timed out after %d of %d events", len(out), n)
		}
	}
	return out
}

func TestPublishStampsEnvelope(t *testing.T) {
	b := newTestBus(t, nil)
	e := publish(t, b, statusEvent("s1", "r1", domain.RunRunning))
	if e.Seq != 1 {
		t.Errorf("seq = %d, want 1", e.Seq)
	}
	if e.ID == "" {
		t.Error("ID not assigned")
	}
	if e.Time.IsZero() {
		t.Error("time not stamped")
	}
	if e.Type != TypeRunStatus {
		t.Errorf("type = %q, want %q (derived from payload)", e.Type, TypeRunStatus)
	}
	if next := publish(t, b, statusEvent("s1", "r1", domain.RunFailed)); next.Seq != 2 {
		t.Errorf("second seq = %d, want 2", next.Seq)
	}
}

func TestPublishValidation(t *testing.T) {
	b := newTestBus(t, nil)
	if _, err := b.Publish(context.Background(), Event{SessionID: "s1"}); !errors.Is(err, ErrNoPayload) {
		t.Errorf("nil payload: got %v, want ErrNoPayload", err)
	}
	if _, err := b.Publish(context.Background(), Event{Payload: PresencePayload{State: PresenceOnline}}); !errors.Is(err, ErrNoSession) {
		t.Errorf("no session: got %v, want ErrNoSession", err)
	}
	_, err := b.Publish(context.Background(), Event{
		SessionID: "s1",
		Type:      TypePresence,
		Payload:   RunStatusPayload{To: domain.RunRunning},
	})
	if err == nil {
		t.Error("mismatched type accepted, want error")
	}
}

func TestFanOutToMultipleSubscribers(t *testing.T) {
	b := newTestBus(t, nil)
	ctx := context.Background()

	all, err := b.Subscribe(ctx, SubscribeOptions{})
	if err != nil {
		t.Fatalf("subscribe all: %v", err)
	}
	s1Only, err := b.Subscribe(ctx, SubscribeOptions{Filter: Filter{Session: "s1"}})
	if err != nil {
		t.Fatalf("subscribe session: %v", err)
	}
	r2Status, err := b.Subscribe(ctx, SubscribeOptions{
		Filter: Filter{Run: "r2", Types: []Type{TypeRunStatus}},
	})
	if err != nil {
		t.Fatalf("subscribe run+type: %v", err)
	}

	publish(t, b, statusEvent("s1", "r1", domain.RunRunning))
	publish(t, b, statusEvent("s2", "r2", domain.RunRunning))
	publish(t, b, Event{SessionID: "s1", RunID: "r2", Payload: RunDiffPayload{}})
	publish(t, b, statusEvent("s1", "r2", domain.RunMerged))

	gotAll := collect(t, all, 4)
	for i, e := range gotAll {
		if e.Seq != uint64(i+1) {
			t.Errorf("all: event %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}

	gotS1 := collect(t, s1Only, 3)
	for _, e := range gotS1 {
		if e.SessionID != "s1" {
			t.Errorf("session filter leaked event %#v", e)
		}
	}
	if gotS1[0].Seq != 1 || gotS1[1].Seq != 3 || gotS1[2].Seq != 4 {
		t.Errorf("session filter seqs = %d,%d,%d, want 1,3,4", gotS1[0].Seq, gotS1[1].Seq, gotS1[2].Seq)
	}

	gotR2 := collect(t, r2Status, 2)
	if gotR2[0].Seq != 2 || gotR2[1].Seq != 4 {
		t.Errorf("run+type filter seqs = %d,%d, want 2,4", gotR2[0].Seq, gotR2[1].Seq)
	}
	for _, e := range gotR2 {
		if e.RunID != "r2" || e.Type != TypeRunStatus {
			t.Errorf("run+type filter leaked event %#v", e)
		}
	}
}

func TestSlowConsumerDoesNotBlockPublisher(t *testing.T) {
	b := newTestBus(t, nil)
	ctx := context.Background()
	const total = 1000
	const stalledBuf = 8

	stalled, err := b.Subscribe(ctx, SubscribeOptions{Buffer: stalledBuf})
	if err != nil {
		t.Fatalf("subscribe stalled: %v", err)
	}
	fast, err := b.Subscribe(ctx, SubscribeOptions{Buffer: total})
	if err != nil {
		t.Fatalf("subscribe fast: %v", err)
	}

	// The stalled subscriber never reads. Publishing must still finish
	// promptly; the watchdog below fails the test if a publish blocks.
	done := make(chan error, 1)
	go func() {
		for i := 0; i < total; i++ {
			if _, err := b.Publish(ctx, statusEvent("s1", "r1", domain.RunRunning)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("publisher blocked by stalled subscriber")
	}

	// The fast subscriber's buffer holds every event, so nothing was lost
	// even though it read nothing while the publisher ran.
	got := collect(t, fast, total)
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Fatalf("fast subscriber: event %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}
	if fast.Dropped() != 0 {
		t.Errorf("fast subscriber dropped %d events, want 0", fast.Dropped())
	}
	// The stalled subscriber holds at most its buffer plus one in-flight
	// event; everything else must have been dropped, and the loss must be
	// observable.
	if d := stalled.Dropped(); d < total-stalledBuf-1 {
		t.Errorf("stalled subscriber dropped %d events, want >= %d", d, total-stalledBuf-1)
	}
}

func TestReplayFromCursorWithGaplessLiveHandoff(t *testing.T) {
	log := newTestLog(t)
	b := newTestBus(t, log)
	ctx := context.Background()
	const persisted = 100
	const live = 100

	var cursor uint64
	for i := 0; i < persisted; i++ {
		e := publish(t, b, statusEvent("s1", "r1", domain.RunRunning))
		if i == persisted/2-1 {
			cursor = e.Seq
		}
	}

	sub, err := b.Subscribe(ctx, SubscribeOptions{
		Filter:   Filter{Session: "s1"},
		Replay:   true,
		AfterSeq: cursor,
		Buffer:   persisted + live,
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Publish live events concurrently with the replay phase to exercise
	// the handoff from persisted to live delivery.
	go func() {
		for i := 0; i < live; i++ {
			b.Publish(ctx, statusEvent("s1", "r1", domain.RunRunning)) //nolint:errcheck
		}
	}()

	wantN := (persisted - int(cursor)) + live
	got := collect(t, sub, wantN)
	for i, e := range got {
		want := cursor + uint64(i+1)
		if e.Seq != want {
			t.Fatalf("event %d: seq %d, want %d (gap or duplicate across the replay/live handoff)", i, e.Seq, want)
		}
	}
	if sub.Dropped() != 0 {
		t.Errorf("dropped %d events, want 0", sub.Dropped())
	}
}

func TestReplayOnlyMatchingSession(t *testing.T) {
	log := newTestLog(t)
	b := newTestBus(t, log)

	publish(t, b, statusEvent("s1", "r1", domain.RunRunning))
	publish(t, b, statusEvent("s2", "r2", domain.RunRunning))
	publish(t, b, statusEvent("s1", "r1", domain.RunMerged))

	sub, err := b.Subscribe(context.Background(), SubscribeOptions{
		Filter: Filter{Session: "s1"},
		Replay: true,
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	got := collect(t, sub, 2)
	if got[0].Seq != 1 || got[1].Seq != 3 {
		t.Errorf("replayed seqs = %d,%d, want 1,3", got[0].Seq, got[1].Seq)
	}
	if got[1].Payload.(RunStatusPayload).To != domain.RunMerged {
		t.Errorf("payload not decoded from log: %#v", got[1].Payload)
	}
}

func TestReplayWithoutLogFails(t *testing.T) {
	b := newTestBus(t, nil)
	if _, err := b.Subscribe(context.Background(), SubscribeOptions{Replay: true}); !errors.Is(err, ErrNoLog) {
		t.Fatalf("got %v, want ErrNoLog", err)
	}
}

func TestSequenceResumesFromLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	log, err := OpenSQLiteLog(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = log.Close() }()

	b1 := newTestBus(t, log)
	publish(t, b1, statusEvent("s1", "r1", domain.RunQueued))
	publish(t, b1, statusEvent("s1", "r1", domain.RunProvisioning))
	if err := b1.Close(); err != nil {
		t.Fatalf("close first bus: %v", err)
	}

	b2 := newTestBus(t, log)
	e := publish(t, b2, statusEvent("s1", "r1", domain.RunRunning))
	if e.Seq != 3 {
		t.Fatalf("seq after restart = %d, want 3", e.Seq)
	}
}

// faultLog wraps an EventLog to inject failures.
type faultLog struct {
	EventLog
	readErr error
	// appendErr, when set, is returned by Append after the wrapped append
	// ran (simulating a driver error reported after the row committed).
	appendErr error
}

func (f *faultLog) Read(ctx context.Context, fl Filter, afterSeq, uptoSeq uint64, limit int) ([]Event, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.EventLog.Read(ctx, fl, afterSeq, uptoSeq, limit)
}

func (f *faultLog) Append(ctx context.Context, e Event) error {
	if err := f.EventLog.Append(ctx, e); err != nil {
		return err
	}
	return f.appendErr
}

func TestReplayFailureSurfacesErr(t *testing.T) {
	readErr := errors.New("disk exploded")
	b := newTestBus(t, &faultLog{EventLog: newTestLog(t), readErr: readErr})
	publish(t, b, statusEvent("s1", "r1", domain.RunRunning))

	sub, err := b.Subscribe(context.Background(), SubscribeOptions{Replay: true})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Fatal("received event despite failing replay")
		}
	case <-time.After(testTimeout):
		t.Fatal("events channel not closed after replay failure")
	}
	if !errors.Is(sub.Err(), readErr) {
		t.Fatalf("Err() = %v, want wrapped %v (failed replay must be distinguishable from a clean close)", sub.Err(), readErr)
	}
}

func TestCancelledReplaySurfacesErr(t *testing.T) {
	log := newTestLog(t)
	b := newTestBus(t, log)
	publish(t, b, statusEvent("s1", "r1", domain.RunRunning))
	publish(t, b, statusEvent("s1", "r1", domain.RunMerged))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sub, err := b.Subscribe(ctx, SubscribeOptions{Replay: true})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	deadline := time.After(testTimeout)
	for {
		select {
		case _, ok := <-sub.Events():
			if !ok {
				if !errors.Is(sub.Err(), context.Canceled) {
					t.Fatalf("Err() = %v, want context.Canceled", sub.Err())
				}
				return
			}
		case <-deadline:
			t.Fatal("events channel not closed after cancelled replay")
		}
	}
}

func TestCleanCloseHasNilErr(t *testing.T) {
	b := newTestBus(t, nil)
	sub, err := b.Subscribe(context.Background(), SubscribeOptions{})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, ok := <-sub.Events(); ok {
		t.Fatal("events channel not closed")
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("Err() after clean close = %v, want nil", err)
	}
}

// TestAppendErrorAfterCommitDoesNotPoisonSequence covers the race where the
// driver reports an error even though the INSERT committed: the bus must
// re-sync its cursor from the log instead of reusing the persisted seq
// (which would violate the primary key on every later publish).
func TestAppendErrorAfterCommitDoesNotPoisonSequence(t *testing.T) {
	log := newTestLog(t)
	fl := &faultLog{EventLog: log, appendErr: context.Canceled}
	b := newTestBus(t, fl)

	if _, err := b.Publish(context.Background(), statusEvent("s1", "r1", domain.RunRunning)); err == nil {
		t.Fatal("publish succeeded despite append error")
	}

	fl.appendErr = nil
	e := publish(t, b, statusEvent("s1", "r1", domain.RunMerged))
	if e.Seq != 2 {
		t.Fatalf("seq after committed-but-errored append = %d, want 2", e.Seq)
	}
}

func TestSubscriptionClose(t *testing.T) {
	b := newTestBus(t, nil)
	sub, err := b.Subscribe(context.Background(), SubscribeOptions{})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	publish(t, b, statusEvent("s1", "r1", domain.RunRunning))
	collect(t, sub, 1)
	if err := sub.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Fatal("received event after close")
		}
	case <-time.After(testTimeout):
		t.Fatal("events channel not closed")
	}
	publish(t, b, statusEvent("s1", "r1", domain.RunMerged))
}

func TestBusClose(t *testing.T) {
	b := newTestBus(t, nil)
	sub, err := b.Subscribe(context.Background(), SubscribeOptions{})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Fatal("unexpected event after bus close")
		}
	case <-time.After(testTimeout):
		t.Fatal("events channel not closed after bus close")
	}
	if _, err := b.Publish(context.Background(), statusEvent("s1", "r1", domain.RunRunning)); !errors.Is(err, ErrBusClosed) {
		t.Fatalf("publish after close: got %v, want ErrBusClosed", err)
	}
	if _, err := b.Subscribe(context.Background(), SubscribeOptions{}); !errors.Is(err, ErrBusClosed) {
		t.Fatalf("subscribe after close: got %v, want ErrBusClosed", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestConcurrentPublishers(t *testing.T) {
	log := newTestLog(t)
	b := newTestBus(t, log)
	ctx := context.Background()
	const publishers = 8
	const perPublisher = 50

	sub, err := b.Subscribe(ctx, SubscribeOptions{Buffer: publishers * perPublisher})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	errs := make(chan error, publishers)
	for p := 0; p < publishers; p++ {
		go func(p int) {
			for i := 0; i < perPublisher; i++ {
				run := domain.RunID(fmt.Sprintf("r%d", p))
				if _, pubErr := b.Publish(ctx, statusEvent("s1", run, domain.RunRunning)); pubErr != nil {
					errs <- pubErr
					return
				}
			}
			errs <- nil
		}(p)
	}
	for p := 0; p < publishers; p++ {
		if pubErr := <-errs; pubErr != nil {
			t.Fatalf("publish: %v", pubErr)
		}
	}

	got := collect(t, sub, publishers*perPublisher)
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Fatalf("event %d: seq %d, want %d", i, e.Seq, i+1)
		}
	}
	last, err := log.LastSeq(ctx)
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if last != publishers*perPublisher {
		t.Fatalf("log last seq = %d, want %d", last, publishers*perPublisher)
	}
}
