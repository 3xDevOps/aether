package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestLaunchDefaultsRoundTripRevisionsAndCascade(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ana, bo := mustCreateMember(t, db), mustCreateTeammate(t, db)
	if _, err := db.LaunchDefaults(ctx, ana.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LaunchDefaults before any save = %v, want ErrNotFound", err)
	}
	saved := LaunchDefaults{
		Agent:          "claude",
		Agents:         map[string]AgentLaunchDefault{"claude": {Mode: domain.LaunchACP, Model: "opus"}, "codex": {Mode: domain.LaunchTUI}},
		Account:        bo.ID,
		FallbackPolicy: "ordered",
		FallbackOrder:  []domain.MemberID{bo.ID, ana.ID},
		Source:         "member",
	}
	if err := db.PutLaunchDefaults(ctx, ana.ID, saved, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.PutLaunchDefaults(ctx, bo.ID, LaunchDefaults{Agent: "codex", Source: "member"}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := db.LaunchDefaults(ctx, ana.ID)
	if err != nil || got.UpdatedAt.IsZero() || got.Revision == 0 {
		t.Fatalf("LaunchDefaults = %+v, %v", got, err)
	}
	saved.Revision, saved.UpdatedAt = got.Revision, got.UpdatedAt
	if !reflect.DeepEqual(*got, saved) {
		t.Fatalf("LaunchDefaults = %+v, want %+v", *got, saved)
	}
	first := got.Revision
	if err := db.PutLaunchDefaults(ctx, ana.ID, LaunchDefaults{Source: "assistant"}, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("PutLaunchDefaults over a saved row with revision 0 = %v, want ErrConflict", err)
	}
	if err := db.PutLaunchDefaults(ctx, ana.ID, LaunchDefaults{Source: "assistant"}, first); err != nil {
		t.Fatal(err)
	}
	if got, err = db.LaunchDefaults(ctx, ana.ID); err != nil || got.Agent != "" || len(got.Agents) != 0 || got.Account != "" ||
		got.Source != "assistant" || got.Revision == first {
		t.Fatalf("replaced LaunchDefaults = %+v, %v; want every field replaced and a new revision", got, err)
	}
	if err := db.PutLaunchDefaults(ctx, ana.ID, LaunchDefaults{Source: "member"}, first); !errors.Is(err, ErrConflict) {
		t.Fatalf("PutLaunchDefaults with a stale revision = %v, want ErrConflict", err)
	}
	if err := db.DeleteLaunchDefaults(ctx, ana.ID, first); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteLaunchDefaults with a stale revision = %v, want ErrConflict", err)
	}
	if err := db.DeleteLaunchDefaults(ctx, ana.ID, got.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LaunchDefaults(ctx, ana.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleared LaunchDefaults = %v, want ErrNotFound", err)
	}
	if err := db.PutLaunchDefaults(ctx, ana.ID, LaunchDefaults{Source: "member"}, got.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("PutLaunchDefaults with the revision of a cleared row = %v, want ErrConflict", err)
	}
	if err := db.DeleteMember(ctx, bo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LaunchDefaults(ctx, bo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted member's LaunchDefaults = %v, want ErrNotFound", err)
	}
	if err := db.PutLaunchDefaults(ctx, "m-nobody", LaunchDefaults{Source: "member"}, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PutLaunchDefaults for no member = %v, want ErrNotFound", err)
	}
}
