package sshd

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

// The push methods over a real control channel, against the real service
// and its real client: a member's subscriptions are theirs alone, removing
// the member removes them, and a test the server cannot deliver answers
// with why.
func TestPushSubscriptionsBelongToTheCaller(t *testing.T) {
	t.Parallel()
	var e *testEnv
	e = newTestEnv(t, func(c *Config) {
		costs, err := cost.New(cost.Config{Store: c.Store, Bus: c.Bus})
		if err != nil {
			t.Fatalf("cost.New: %v", err)
		}
		svc, err := push.New(push.Config{
			Store: c.Store.(*store.DB), Bus: c.Bus, Runs: c.Runs, Budgets: costs,
			Swarm: func(ctx context.Context, run *domain.Run) (*protocol.AttentionItem, domain.MemberID, error) {
				return e.srv.SwarmAttention(ctx, run)
			},
			KeyPath: filepath.Join(t.TempDir(), "push", "vapid_key.pem"),
		})
		if err != nil {
			t.Fatalf("push.New: %v", err)
		}
		c.Services.Push = svc
	})
	bobSigner, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	ada, bobControl := controlClient(t, e), controlAs(t, e, bobSigner)

	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate browser key: %v", err)
	}
	b64 := base64.RawURLEncoding
	// Loopback, which the server must refuse to post to.
	device := protocol.PushSubscribeParams{
		Endpoint: "https://127.0.0.1:9/send/device",
		Keys:     protocol.PushKeys{P256DH: b64.EncodeToString(key.PublicKey().Bytes()), Auth: b64.EncodeToString(make([]byte, 16))},
	}
	here := protocol.PushEndpointParams{Endpoint: device.Endpoint}
	status := func(c *protocol.Client) protocol.PushStatusResult {
		t.Helper()
		var out protocol.PushStatusResult
		if callErr := c.Call(protocol.MethodPushStatus, protocol.PushStatusParams{Endpoint: device.Endpoint}, &out); callErr != nil {
			t.Fatalf("push.status: %v", callErr)
		}
		return out
	}

	before := status(bobControl)
	point, err := b64.DecodeString(before.PublicKey)
	if err != nil || len(point) != 65 || before.Subscribed {
		t.Fatalf("push.status before subscribing = %+v (%v), want the server's key and not subscribed", before, err)
	}
	var pe *protocol.Error
	bad := device
	bad.Endpoint = "http://push.example/send/device"
	if err = bobControl.Call(protocol.MethodPushSubscribe, bad, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("subscribing a plain-http endpoint = %v, want CodeInvalidParams", err)
	}
	if err = bobControl.Call(protocol.MethodPushSubscribe, device, nil); err != nil {
		t.Fatalf("push.subscribe: %v", err)
	}
	if !status(bobControl).Subscribed || status(ada).Subscribed {
		t.Fatal("after Bob subscribed, push.status must say so for Bob and not for Ada")
	}
	for _, method := range []string{protocol.MethodPushTest, protocol.MethodPushUnsubscribe} {
		if err = ada.Call(method, here, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
			t.Fatalf("Ada's %s on Bob's device = %v, want CodeNotFound", method, err)
		}
	}
	err = bobControl.Call(protocol.MethodPushTest, here, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable ||
		!strings.Contains(pe.Message, "127.0.0.1:9: dial tcp 127.0.0.1:9: refusing to connect to 127.0.0.1: not a public address") {
		t.Fatalf("push.test to a loopback endpoint = %v, want CodeUnavailable carrying the refusal", err)
	}
	if err = bobControl.Call(protocol.MethodPushActive, struct{}{}, nil); err != nil {
		t.Fatalf("push.active: %v", err)
	}

	if err = ada.Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(bob.ID)}, nil); err != nil {
		t.Fatalf("member.remove: %v", err)
	}
	subs, err := e.store.ListPushSubscriptions(context.Background(), bob.ID)
	if err != nil || len(subs) != 0 {
		t.Fatalf("subscriptions after member.remove = %+v, %v; want none", subs, err)
	}
}

type swarmSink chan string

func (s swarmSink) Need(_ context.Context, _ *domain.Run, n push.Need) error {
	s <- fmt.Sprintf("%s: %s (swarm %s)", n.Member, n.Body, n.Mission)
	return nil
}

func (s swarmSink) Clear(context.Context, domain.RunID) error {
	s <- "nothing"
	return nil
}

func (swarmSink) Notice(context.Context, domain.MemberID, push.Notice) error { return nil }

// expect skips the clears of reads that found no need, unless it waits for
// one.
func (s swarmSink) expect(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-s:
			if got == want {
				return
			}
			if got != "nothing" {
				t.Fatalf("sink heard %q, want %q", got, want)
			}
		case <-deadline:
			t.Fatalf("sink did not hear %q", want)
		}
	}
}

// A swarm's question and its stopped integrator each reach the swarm's
// accountable human once, through the real Needs you rows, and nobody
// else: not the integrator's owner, who is an admin, nor another member.
// Archiving the stopped integrator takes it off Needs you, and so clears it.
func TestPushTellsOnlyTheSwarmsHuman(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	db := e.store.(*store.DB)
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	_, carol := addMember(t, e, "Carol", domain.RoleCollaborator, false)
	mission := &domain.Mission{WorkspaceID: e.ws.ID, Objective: "Ship the checkout", AccountableHumanID: bob.ID,
		IntegratorRunOwnerID: e.member.ID, IdempotencyKey: "swarm-1",
		Integrator: domain.MissionIntegrator{AccountMemberID: e.member.ID, Harness: "claude", Mode: domain.LaunchTUI}}
	if err := db.CreateMission(ctx, mission); err != nil {
		t.Fatalf("CreateMission: %v", err)
	}
	integrator := &domain.Run{ID: mission.CurrentIntegratorRunID, WorkspaceID: e.ws.ID, MemberID: e.member.ID, MissionID: mission.ID,
		MissionRole: "integrator", Task: mission.Objective, Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := db.CreateRunWithID(ctx, integrator); err != nil {
		t.Fatalf("CreateRunWithID: %v", err)
	}

	// The devices are loopback, which the real client refuses, and each
	// presents its own self-signed certificate.
	insecure := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test servers only
	}}
	costs, err := cost.New(cost.Config{Store: db, Bus: e.bus})
	if err != nil {
		t.Fatalf("cost.New: %v", err)
	}
	svc, err := push.New(push.Config{Store: db, Bus: e.bus, Runs: e.runs, Swarm: e.srv.SwarmAttention, Budgets: costs,
		KeyPath: filepath.Join(t.TempDir(), "vapid_key.pem"), Client: insecure, Quiet: time.Millisecond})
	if err != nil {
		t.Fatalf("push.New: %v", err)
	}
	heard := make(swarmSink, 16)
	svc.UseSink(heard)
	if err = svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	b64 := base64.RawURLEncoding
	pushes := map[domain.MemberID]*atomic.Int32{}
	for _, member := range []domain.MemberID{e.member.ID, bob.ID, carol.ID} {
		received := new(atomic.Int32)
		device := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			received.Add(1)
			w.WriteHeader(http.StatusCreated)
		}))
		t.Cleanup(device.Close)
		key, keyErr := ecdh.P256().GenerateKey(rand.Reader)
		if keyErr != nil {
			t.Fatalf("generate browser key: %v", keyErr)
		}
		if err = svc.Subscribe(ctx, member, device.URL+"/send/device", b64.EncodeToString(key.PublicKey().Bytes()), b64.EncodeToString(make([]byte, 16))); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		pushes[member] = received
	}
	// The sink hears a need after every device had it.
	pushed := func(want int32) {
		t.Helper()
		for member, got := range pushes {
			if member != bob.ID && got.Load() != 0 || member == bob.ID && got.Load() != want {
				t.Fatalf("%s got %d pushes; want %d for Bob and none for anyone else", member, got.Load(), want)
			}
		}
	}
	changed := events.Event{WorkspaceID: e.ws.ID, Payload: events.MissionChangedPayload{MissionID: mission.ID}}
	publish := func(ev events.Event) {
		t.Helper()
		if _, pubErr := e.bus.Publish(ctx, ev); pubErr != nil {
			t.Fatalf("publish: %v", pubErr)
		}
	}

	q, err := db.InsertMissionQuestion(ctx, mission.ID, integrator.ID, "Which branch?", "ask-1")
	if err != nil {
		t.Fatalf("InsertMissionQuestion: %v", err)
	}
	publish(changed)
	heard.expect(t, fmt.Sprintf("%s: The integrator asks: Which branch? (swarm %s)", bob.ID, mission.ID))
	pushed(1)
	publish(changed)
	if _, _, err = db.AnswerMissionQuestion(ctx, q.ID, bob.ID, "main", "answer-1"); err != nil {
		t.Fatalf("AnswerMissionQuestion: %v", err)
	}
	publish(changed)
	heard.expect(t, "nothing")
	// The answered question's notification is replaced on Bob's device.
	pushed(2)

	if _, err = db.UpdateRunStatus(ctx, integrator.ID, domain.RunFailed, "agent exited 1", "", nil, nil); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	publish(events.Event{WorkspaceID: e.ws.ID, RunID: integrator.ID, Payload: events.RunStatusPayload{To: domain.RunFailed, Reason: "agent exited 1"}})
	heard.expect(t, fmt.Sprintf("%s: Integrator stopped, replace it to continue (swarm %s)", bob.ID, mission.ID))
	pushed(3)

	at := time.Now()
	if _, err = db.SetRunArchived(ctx, integrator.ID, &at); err != nil {
		t.Fatalf("SetRunArchived: %v", err)
	}
	publish(events.Event{WorkspaceID: e.ws.ID, RunID: integrator.ID, Payload: events.RunArchivedPayload{}})
	heard.expect(t, "nothing")
}
