package store

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestPushSubscriptions(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ada := mustCreateMember(t, db)
	bob := &domain.Member{DisplayName: "Bob", PublicKey: testKey(t, "bob@laptop"), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, bob); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}

	phone := &PushSubscription{Endpoint: "https://push.example/phone", MemberID: ada.ID, P256DH: "key-1", Auth: "auth-1"}
	laptop := &PushSubscription{Endpoint: "https://push.example/laptop", MemberID: ada.ID, P256DH: "key-2", Auth: "auth-2"}
	for _, sub := range []*PushSubscription{phone, laptop} {
		if err := db.PutPushSubscription(ctx, sub); err != nil {
			t.Fatalf("PutPushSubscription: %v", err)
		}
	}
	// A browser that subscribes again keeps one row, with its new keys.
	phone.P256DH = "key-3"
	if err := db.PutPushSubscription(ctx, phone); err != nil {
		t.Fatalf("PutPushSubscription again: %v", err)
	}
	got, err := db.ListPushSubscriptions(ctx, ada.ID)
	if err != nil || len(got) != 2 || *got[0] != *phone || *got[1] != *laptop {
		t.Fatalf("ListPushSubscriptions = %+v, %v; want the phone then the laptop", got, err)
	}

	if err := db.DeletePushSubscription(ctx, bob.ID, phone.Endpoint); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting another member's subscription = %v, want ErrNotFound", err)
	}
	if err := db.DeletePushSubscription(ctx, ada.ID, phone.Endpoint); err != nil {
		t.Fatalf("DeletePushSubscription: %v", err)
	}
	if err := db.DeleteMember(ctx, ada.ID); err != nil {
		t.Fatalf("DeleteMember: %v", err)
	}
	if got, err := db.ListPushSubscriptions(ctx, ada.ID); err != nil || len(got) != 0 {
		t.Fatalf("subscriptions after the member was removed = %+v, %v; want none", got, err)
	}
	if err := db.PutPushSubscription(ctx, laptop); !errors.Is(err, ErrNotFound) {
		t.Fatalf("subscribing a removed member = %v, want ErrNotFound", err)
	}
}
