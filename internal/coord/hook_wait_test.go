package coord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type hookReply struct {
	status protocol.CoordStatusResult
	raw    json.RawMessage
	err    error
}

func allowHookWake(_ context.Context, _ domain.RunID, dispatch func() error) error {
	return dispatch()
}

func startHookCall(t *testing.T, h *coordHarness, run domain.RunID, params any) <-chan hookReply {
	t.Helper()
	client := h.dial(t, run)
	done := make(chan hookReply, 1)
	go func() {
		var reply hookReply
		reply.err = client.Call(protocol.MethodCoordHookStatus, params, &reply.raw)
		if reply.err == nil {
			reply.err = json.Unmarshal(reply.raw, &reply.status)
		}
		done <- reply
	}()
	return done
}

func awaitHookReply(t *testing.T, done <-chan hookReply) hookReply {
	t.Helper()
	select {
	case reply := <-done:
		if reply.err != nil {
			t.Fatalf("hook response: %v", reply.err)
		}
		return reply
	case <-time.After(3 * time.Second):
		t.Fatal("hook response did not arrive")
		return hookReply{}
	}
}

func waitHookRegistration(t *testing.T, h *coordHarness, run domain.RunID, inbox bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		h.svc.mu.Lock()
		ready := len(h.svc.hookWaiters[run]) > 0
		if inbox {
			ready = h.svc.inboxConsumers[run] > 0
		}
		h.svc.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("observer/consumer did not register")
		}
	}
}

func provisionHookRun(t *testing.T, h *coordHarness, run domain.RunID) {
	t.Helper()
	if _, err := h.svc.Provision(context.Background(), run, nil); err != nil {
		t.Fatal(err)
	}
}

func TestHookWaitSocketObservesWithoutDelivery(t *testing.T) {
	h := newHarness(t, 2, func(c *Config) { c.WakeAdmission = allowHookWake })
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	provisionHookRun(t, h, b)
	done := startHookCall(t, h, b, protocol.CoordHookStatusParams{WaitSeconds: 30})
	waitHookRegistration(t, h, b, false)
	sent, err := h.svc.Send(context.Background(), a, sendParams(b, "private peer body"))
	if err != nil {
		t.Fatal(err)
	}
	reply := awaitHookReply(t, done)
	if !reply.status.WaitSupported || !reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 || reply.status.UnreadMessageIDs[0] != sent.MessageID {
		t.Fatalf("observer = %+v", reply.status)
	}
	if strings.Contains(string(reply.raw), "private peer body") || strings.Contains(string(reply.raw), "ack_token") || strings.Contains(string(reply.raw), "delivery_token") {
		t.Fatalf("observer leaked mailbox payload: %s", reply.raw)
	}
	stored, getErr := h.db.GetRunMessage(context.Background(), sent.MessageID)
	if getErr != nil || stored.DeliveryToken != "" || stored.DeliveredAt != nil || stored.AckedAt != nil {
		t.Fatalf("observer changed delivery state: %+v, %v", stored, getErr)
	}
	inbox, inboxErr := h.svc.Inbox(context.Background(), b, protocol.CoordInboxParams{})
	if inboxErr != nil || len(inbox.Messages) != 1 || inbox.Messages[0].Body != "private peer body" || inbox.Messages[0].FromRunID != string(a) || inbox.AckToken == "" {
		t.Fatalf("inbox = %+v, %v", inbox, inboxErr)
	}
	redelivery, inboxErr := h.svc.Inbox(context.Background(), b, protocol.CoordInboxParams{})
	if inboxErr != nil || redelivery.AckToken != inbox.AckToken || len(redelivery.Messages) != 1 {
		t.Fatalf("observer lost durable redelivery: %+v, %v", redelivery, inboxErr)
	}
}

func TestHookWaitUnchangedIDsWaitAndSameCountReplacementWakes(t *testing.T) {
	h := newHarness(t, 2, func(c *Config) { c.WakeAdmission = allowHookWake })
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	provisionHookRun(t, h, b)
	first, err := h.svc.Send(context.Background(), a, sendParams(b, "first"))
	if err != nil {
		t.Fatal(err)
	}
	// An unchanged observed set is not an immediate response, but timeout
	// still admits it so release of a hold needs no new message/cursor.
	done := startHookCall(t, h, b, protocol.CoordHookStatusParams{WaitSeconds: 1, SeenMessageIDs: []string{first.MessageID}})
	waitHookRegistration(t, h, b, false)
	select {
	case reply := <-done:
		t.Fatalf("unchanged IDs did not wait: %+v", reply)
	case <-time.After(50 * time.Millisecond):
	}
	if reply := awaitHookReply(t, done); !reply.status.WakeAdmitted || reply.status.UnreadMessageIDs[0] != first.MessageID {
		t.Fatalf("timeout failed to reconsider unchanged IDs: %+v", reply.status)
	}
	batch, inboxErr := h.svc.Inbox(context.Background(), b, protocol.CoordInboxParams{})
	if inboxErr != nil {
		t.Fatal(inboxErr)
	}
	if _, inboxErr = h.svc.Inbox(context.Background(), b, protocol.CoordInboxParams{AckToken: batch.AckToken}); inboxErr != nil {
		t.Fatal(inboxErr)
	}
	second, err := h.svc.Send(context.Background(), a, sendParams(b, "second"))
	if err != nil {
		t.Fatal(err)
	}
	reply := awaitHookReply(t, startHookCall(t, h, b, protocol.CoordHookStatusParams{WaitSeconds: 30, SeenMessageIDs: []string{first.MessageID}}))
	if !reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 || reply.status.UnreadMessageIDs[0] != second.MessageID {
		t.Fatalf("same-count replacement was missed: %+v", reply.status)
	}
}

func TestHookWaitInboxConsumerKeepsPriorityAfterReturning(t *testing.T) {
	admitting, resume := make(chan struct{}), make(chan struct{})
	h := newHarness(t, 2, func(c *Config) {
		c.WakeAdmission = func(_ context.Context, _ domain.RunID, dispatch func() error) error {
			close(admitting)
			<-resume
			return dispatch()
		}
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	provisionHookRun(t, h, b)
	hookDone := startHookCall(t, h, b, protocol.CoordHookStatusParams{WaitSeconds: 30})
	waitHookRegistration(t, h, b, false)
	inboxClient := h.dial(t, b)
	inboxDone := make(chan error, 1)
	var inbox protocol.CoordInboxResult
	go func() {
		inboxDone <- inboxClient.Call(protocol.MethodCoordInbox, protocol.CoordInboxParams{WaitSeconds: 30}, &inbox)
	}()
	waitHookRegistration(t, h, b, true)
	if _, err := h.svc.Send(context.Background(), a, sendParams(b, "consumer owns this body")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-inboxDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inbox waiter did not receive mail")
	}
	select {
	case <-admitting:
	case <-time.After(3 * time.Second):
		t.Fatal("hook did not reach admission")
	}
	close(resume)
	reply := awaitHookReply(t, hookDone)
	if reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 || len(inbox.Messages) != 1 || inbox.Messages[0].Body != "consumer owns this body" || inbox.AckToken == "" {
		t.Fatalf("consumer precedence lost: hook %+v, inbox %+v", reply.status, inbox)
	}
}

func TestHookWaitHeldIDsAreAdmittedAfterRelease(t *testing.T) {
	var held atomic.Bool
	held.Store(true)
	h := newHarness(t, 2, func(c *Config) {
		c.WakeAdmission = func(_ context.Context, _ domain.RunID, dispatch func() error) error {
			if held.Load() {
				return errors.New("held")
			}
			return dispatch()
		}
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	provisionHookRun(t, h, b)
	if _, err := h.svc.Send(context.Background(), a, sendParams(b, "held")); err != nil {
		t.Fatal(err)
	}
	first := awaitHookReply(t, startHookCall(t, h, b, protocol.CoordHookStatusParams{}))
	if first.status.WakeAdmitted || len(first.status.UnreadMessageIDs) != 1 {
		t.Fatalf("held observer = %+v", first.status)
	}
	held.Store(false)
	second := awaitHookReply(t, startHookCall(t, h, b, protocol.CoordHookStatusParams{WaitSeconds: 1, SeenMessageIDs: first.status.UnreadMessageIDs}))
	if !second.status.WakeAdmitted || second.status.UnreadMessageIDs[0] != first.status.UnreadMessageIDs[0] {
		t.Fatalf("release failed to admit observed mail: %+v", second.status)
	}
}

func TestHookWaitReleaseAndCloseCancelPromptly(t *testing.T) {
	for _, release := range []bool{false, true} {
		name := "close"
		if release {
			name = "release"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 1)
			run := h.run(0)
			provisionHookRun(t, h, run)
			done := startHookCall(t, h, run, protocol.CoordHookStatusParams{WaitSeconds: 30})
			waitHookRegistration(t, h, run, false)
			stopped := make(chan error, 1)
			go func() {
				if release {
					stopped <- h.svc.Release(run)
				} else {
					stopped <- h.svc.Close()
				}
			}()
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("teardown waited for the 30-second observer timeout")
			}
			select {
			case reply := <-done:
				if reply.status.WakeAdmitted {
					t.Fatal("teardown admitted a wake")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("observer retained a run reference after teardown")
			}
		})
	}
}

type legacyObserverMail struct{ store.MessageStore }

func TestHookWaitValidationAndLegacyCompatibility(t *testing.T) {
	h := newHarness(t, 1)
	run := h.run(0)
	provisionHookRun(t, h, run)
	for _, method := range []string{protocol.MethodCoordStatus, protocol.MethodCoordHookStatus} {
		var raw map[string]json.RawMessage
		if err := h.dial(t, run).Call(method, nil, &raw); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"wait_supported", "unread_message_ids", "wake_admitted"} {
			if _, ok := raw[field]; ok {
				t.Fatalf("legacy %s gained %s", method, field)
			}
		}
	}
	for _, params := range []protocol.CoordHookStatusParams{
		{WaitSeconds: -1}, {WaitSeconds: 31},
		{SeenMessageIDs: []string{""}}, {SeenMessageIDs: []string{"bad\nID"}},
		{SeenMessageIDs: []string{strings.Repeat("a", protocol.CoordMaxMessageIDBytes+1)}},
		{SeenMessageIDs: make([]string, protocol.CoordMaxUnread+1)},
	} {
		var out protocol.CoordStatusResult
		err := h.dial(t, run).Call(protocol.MethodCoordHookStatus, params, &out)
		var rpcErr *protocol.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams {
			t.Fatalf("invalid params %+v = %v", params, err)
		}
	}
	// Maximum-size opaque IDs and exactly the cap are accepted; duplicate
	// observations remain a set rather than creating extra unread entries.
	ids := make([]string, protocol.CoordMaxUnread)
	for i := range ids {
		ids[i] = strings.Repeat("a", protocol.CoordMaxMessageIDBytes)
	}
	bounded := awaitHookReply(t, startHookCall(t, h, run, protocol.CoordHookStatusParams{SeenMessageIDs: ids}))
	if !bounded.status.WaitSupported || bounded.status.WakeAdmitted || len(bounded.status.UnreadMessageIDs) != 0 {
		t.Fatalf("bounded empty observation = %+v", bounded.status)
	}
	legacy := newHarness(t, 1, func(c *Config) { c.Mail = legacyObserverMail{c.Mail} })
	provisionHookRun(t, legacy, legacy.run(0))
	reply := awaitHookReply(t, startHookCall(t, legacy, legacy.run(0), protocol.CoordHookStatusParams{WaitSeconds: 30}))
	if reply.status.WaitSupported || reply.status.WakeAdmitted {
		t.Fatalf("legacy store claimed support: %+v", reply.status)
	}
}

type snapshotGateMail struct {
	store.MessageStore
	ids      store.UnackedRunMessageIDsStore
	snapshot chan struct{}
	resume   chan struct{}
	pause    atomic.Bool
}

func (m *snapshotGateMail) ListUnackedRunMessageIDs(ctx context.Context, run domain.RunID, limit int) ([]string, error) {
	ids, err := m.ids.ListUnackedRunMessageIDs(ctx, run, limit)
	if m.pause.CompareAndSwap(true, false) {
		close(m.snapshot)
		select {
		case <-m.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return ids, err
}

func TestHookWaitAppendDuringInitialSnapshotIsNotLost(t *testing.T) {
	gate := &snapshotGateMail{snapshot: make(chan struct{}), resume: make(chan struct{})}
	gate.pause.Store(true)
	h := newHarness(t, 2, func(c *Config) {
		gate.MessageStore = c.Mail
		gate.ids = c.Mail.(store.UnackedRunMessageIDsStore)
		c.Mail, c.WakeAdmission = gate, allowHookWake
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	provisionHookRun(t, h, b)
	done := startHookCall(t, h, b, protocol.CoordHookStatusParams{WaitSeconds: 30})
	select {
	case <-gate.snapshot:
	case <-time.After(3 * time.Second):
		t.Fatal("snapshot not reached")
	}
	sent, err := h.svc.Send(context.Background(), a, sendParams(b, "arrived after empty snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	close(gate.resume)
	reply := awaitHookReply(t, done)
	if !reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 || reply.status.UnreadMessageIDs[0] != sent.MessageID {
		t.Fatalf("append in snapshot gap lost: %+v", reply.status)
	}
}

func TestHookWaitConsumerDuringAdmissionSnapshotKeepsPriority(t *testing.T) {
	gate := &snapshotGateMail{snapshot: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(gate.resume) })
	defer resume()
	h := newHarness(t, 2, func(c *Config) {
		gate.MessageStore = c.Mail
		gate.ids = c.Mail.(store.UnackedRunMessageIDsStore)
		c.Mail = gate
		c.WakeAdmission = func(_ context.Context, _ domain.RunID, dispatch func() error) error {
			gate.pause.Store(true)
			return dispatch()
		}
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	provisionHookRun(t, h, b)
	sent, err := h.svc.Send(context.Background(), a, sendParams(b, "consumer during admission"))
	if err != nil {
		t.Fatal(err)
	}
	done := startHookCall(t, h, b, protocol.CoordHookStatusParams{})
	select {
	case <-gate.snapshot:
	case <-time.After(3 * time.Second):
		t.Fatal("admission snapshot not reached")
	}
	// This consumer registers, receives, and leaves while the admission-time
	// snapshot is paused. Its priority must survive the consumer count reset.
	inbox, rpcErr := h.svc.Inbox(context.Background(), b, protocol.CoordInboxParams{WaitSeconds: 30})
	if rpcErr != nil || len(inbox.Messages) != 1 || inbox.Messages[0].ID != sent.MessageID || inbox.AckToken == "" {
		t.Fatalf("consumer during snapshot = %+v, %v", inbox, rpcErr)
	}
	resume()
	reply := awaitHookReply(t, done)
	if reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 || reply.status.UnreadMessageIDs[0] != sent.MessageID {
		t.Fatalf("finished consumer lost admission priority: %+v", reply.status)
	}
}

func TestHookWaitComparesMembershipNotOrder(t *testing.T) {
	h := newHarness(t, 2, func(c *Config) { c.WakeAdmission = allowHookWake })
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	provisionHookRun(t, h, b)
	first, err := h.svc.Send(context.Background(), a, sendParams(b, "one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.Send(context.Background(), a, sendParams(b, "two"))
	if err != nil {
		t.Fatal(err)
	}
	done := startHookCall(t, h, b, protocol.CoordHookStatusParams{WaitSeconds: 1, SeenMessageIDs: []string{second.MessageID, first.MessageID}})
	waitHookRegistration(t, h, b, false)
	select {
	case reply := <-done:
		t.Fatalf("reordered set woke immediately: %+v", reply)
	case <-time.After(50 * time.Millisecond):
	}
	reply := awaitHookReply(t, done)
	if !reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 2 {
		t.Fatalf("same set after timeout = %+v", reply.status)
	}
}
