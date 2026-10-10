package sshd

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
)

// The push methods over a real control channel, against the real service
// and its real client: a member's subscriptions are theirs alone, removing
// the member removes them, and a test the server cannot deliver answers
// with why.
func TestPushSubscriptionsBelongToTheCaller(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) {
		svc, err := push.New(push.Config{
			Store: c.Store, Bus: c.Bus, Runs: c.Runs,
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
