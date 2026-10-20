package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type recordingBus struct {
	mu     sync.Mutex
	events []events.Event
}

func (b *recordingBus) Publish(_ context.Context, e events.Event) (events.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	return e, nil
}

func (b *recordingBus) Subscribe(context.Context, events.SubscribeOptions) (events.Subscription, error) {
	return nil, errors.New("unused")
}

func (b *recordingBus) Close() error { return nil }

func (b *recordingBus) changes() []events.IntegrationChangedPayload {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []events.IntegrationChangedPayload
	for _, e := range b.events {
		if p, ok := e.Payload.(events.IntegrationChangedPayload); ok && e.WorkspaceID == "workspace-1" {
			out = append(out, p)
		}
	}
	return out
}

// expectChanges checks that every save since the fixture's version 1 row
// published, and that the newest event names the candidate as stored now.
func expectChanges(t *testing.T, step string, bus *recordingBus, st *deliveryTestStore, mission domain.MissionID) {
	t.Helper()
	changes := bus.changes()
	want := events.IntegrationChangedPayload{MissionID: mission, CandidateID: "candidate-1", State: st.record.State, Version: st.record.Version}
	if int64(len(changes)) != st.record.Version-1 || changes[len(changes)-1] != want {
		t.Fatalf("%s: integration.changed = %+v, want %d events ending with %+v", step, changes, st.record.Version-1, want)
	}
}

func TestDeliveryWritesPublishIntegrationChanged(t *testing.T) {
	service, st, _ := newDeliveryTestService(t, protocol.DeliveryPending)
	service.admission = func(context.Context, Admission) (func(), error) { return func() {}, nil }
	candidate := serviceCandidate(t, service)
	candidate.MissionID = "mission-1"
	candidate.DeliveryRequest = nil
	storeCandidate(t, service, candidate)
	bus := &recordingBus{}
	service.bus = bus
	ctx := context.Background()
	owner := Actor{MemberID: "owner-1"}

	requested, err := service.RequestDelivery(ctx, owner, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", CandidateRevision: "candidate-revision-1",
		VerificationIDs: []string{"verification-1"}, Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "request",
	})
	if err != nil {
		t.Fatal(err)
	}
	expectChanges(t, "request delivery", bus, st, "mission-1")
	r := requested.DeliveryRequest
	if _, err = service.Decide(ctx, owner, protocol.IntegrationDecideParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: r.RequestID, RequestVersion: r.RequestVersion, Approve: true,
	}); err != nil {
		t.Fatal(err)
	}
	expectChanges(t, "decide", bus, st, "mission-1")
	if _, err = service.Deliver(ctx, owner, protocol.IntegrationDeliverParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: r.RequestID, RequestVersion: r.RequestVersion,
	}); err != nil {
		t.Fatal(err)
	}
	delivered := serviceCandidate(t, service)
	if delivered.DeliveryReceipt == nil {
		t.Fatalf("delivered candidate = %+v, want a receipt", delivered)
	}
	expectChanges(t, "deliver", bus, st, "mission-1")
}

// The final verification write happens off the request goroutine, after
// Verify has returned.
func TestVerificationResultPublishesIntegrationChanged(t *testing.T) {
	service, st, _ := newDeliveryTestService(t, protocol.DeliveryPending)
	service.root = t.TempDir()
	service.ctx = context.Background()
	service.cancel = func() {}
	bus := &recordingBus{}
	service.bus = bus
	now := service.now()
	exit := 0
	candidate := serviceCandidate(t, service)
	candidate.Verifications[0] = protocol.Verification{
		VerificationID: "verification-1", CandidateRevision: candidate.CandidateRevision,
		Status: protocol.VerificationRunning, CreatedAt: now, ExpiresAt: now.Add(time.Hour), CreationKey: "creation-key-1",
	}
	storeCandidate(t, service, candidate)
	service.finishVerification(context.Background(), "workspace-1", "candidate-1", "verification-1",
		protocol.VerificationPassed, &exit, []byte("ok"), false, nil)
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if v := serviceCandidate(t, service).Verifications[0]; v.Status != protocol.VerificationPassed {
		t.Fatalf("verification = %+v, want passed", v)
	}
	expectChanges(t, "finish verification", bus, st, "")
}
