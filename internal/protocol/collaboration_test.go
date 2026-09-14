package protocol

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/store"
)

func TestRoomMessageFromStorePreservesTimestampPrecisionAndActorSnapshot(t *testing.T) {
	created := time.Date(2026, time.September, 14, 12, 34, 56, 123456789, time.FixedZone("test", -5*60*60))
	updated := created.Add(987654321 * time.Nanosecond)
	message := RoomMessageFromStore(&store.RoomMessage{
		ActorID:          "member-former",
		ActorDisplayName: "Former participant",
		CreatedAt:        created,
		UpdatedAt:        updated,
	})
	if message.ActorID != "member-former" || message.ActorDisplayName != "Former participant" {
		t.Fatalf("actor attribution = %q/%q, want member-former/Former participant",
			message.ActorID, message.ActorDisplayName)
	}

	tests := []struct {
		name string
		wire string
		want time.Time
	}{
		{name: "created_at", wire: message.CreatedAt, want: created},
		{name: "updated_at", wire: message.UpdatedAt, want: updated},
	}
	for _, tc := range tests {
		parsed, err := time.Parse(time.RFC3339Nano, tc.wire)
		if err != nil {
			t.Fatalf("%s %q is not RFC3339Nano: %v", tc.name, tc.wire, err)
		}
		if !parsed.Equal(tc.want) {
			t.Errorf("%s parsed as %s, want %s", tc.name, parsed, tc.want)
		}
	}
}

func TestCollaborationTimeKeepsSecondAlignedText(t *testing.T) {
	secondAligned := time.Date(2026, time.September, 14, 12, 34, 56, 0, time.UTC)
	if got, want := collaborationTime(secondAligned), "2026-09-14T12:34:56Z"; got != want {
		t.Fatalf("collaborationTime() = %q, want %q", got, want)
	}
}
