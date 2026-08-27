package store

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func testEnvironmentDefinition(workspace domain.WorkspaceID) *domain.EnvironmentDefinition {
	return &domain.EnvironmentDefinition{
		WorkspaceID: workspace,
		Dockerfile:  "FROM ubuntu:24.04\nRUN apt-get update && apt-get install -y ripgrep=14.1.0-1\n",
		Manifest: []domain.ManifestItem{{
			Name: "ripgrep", Version: "14.1.0", Reason: "code search",
			StartLine: 2, EndLine: 2, CheckCommand: "rg --version",
		}},
		Source: domain.EnvironmentSourceManual,
		Status: domain.EnvironmentSaved,
	}
}

// TestEnvironmentDefinitionSaveAssignsVersions covers version assignment
// (max+1 per workspace, independent across workspaces) and the stamped
// timestamps.
func TestEnvironmentDefinitionSaveAssignsVersions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)

	first := testEnvironmentDefinition(w.ID)
	if err := db.SaveEnvironmentDefinition(ctx, first); err != nil {
		t.Fatalf("SaveEnvironmentDefinition: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("first version = %d, want 1", first.Version)
	}
	if first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Fatalf("save did not stamp timestamps: %+v", first)
	}

	second := testEnvironmentDefinition(w.ID)
	if err := db.SaveEnvironmentDefinition(ctx, second); err != nil {
		t.Fatalf("SaveEnvironmentDefinition (second): %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second version = %d, want 2", second.Version)
	}

	// A different workspace starts its own counter.
	other := &domain.Workspace{Name: "other"}
	if err := db.CreateWorkspace(ctx, other); err != nil {
		t.Fatalf("CreateWorkspace (other): %v", err)
	}
	elsewhere := testEnvironmentDefinition(other.ID)
	if err := db.SaveEnvironmentDefinition(ctx, elsewhere); err != nil {
		t.Fatalf("SaveEnvironmentDefinition (other workspace): %v", err)
	}
	if elsewhere.Version != 1 {
		t.Fatalf("other workspace version = %d, want 1", elsewhere.Version)
	}

	// Saving against a missing workspace surfaces the missing parent.
	if err := db.SaveEnvironmentDefinition(ctx, testEnvironmentDefinition("ws_missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("save for missing workspace = %v, want ErrNotFound", err)
	}

	// An invalid definition never reaches the database.
	invalid := testEnvironmentDefinition(w.ID)
	invalid.Dockerfile = ""
	if err := db.SaveEnvironmentDefinition(ctx, invalid); err == nil {
		t.Fatal("save of invalid definition succeeded, want error")
	}
}

// TestEnvironmentDefinitionGetRoundTrip covers get-by-version fidelity and
// the not-found contract.
func TestEnvironmentDefinitionGetRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)

	d := testEnvironmentDefinition(w.ID)
	if err := db.SaveEnvironmentDefinition(ctx, d); err != nil {
		t.Fatalf("SaveEnvironmentDefinition: %v", err)
	}

	got, err := db.GetEnvironmentDefinition(ctx, w.ID, d.Version)
	if err != nil {
		t.Fatalf("GetEnvironmentDefinition: %v", err)
	}
	if got.WorkspaceID != w.ID || got.Version != 1 || got.Dockerfile != d.Dockerfile ||
		got.Source != d.Source || got.Status != domain.EnvironmentSaved {
		t.Fatalf("round trip = %+v, want the saved definition", got)
	}
	if len(got.Manifest) != 1 || got.Manifest[0] != d.Manifest[0] {
		t.Fatalf("manifest round trip = %+v, want %+v", got.Manifest, d.Manifest)
	}
	if !got.CreatedAt.Equal(d.CreatedAt) || !got.UpdatedAt.Equal(d.UpdatedAt) {
		t.Fatalf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, d.CreatedAt, d.UpdatedAt)
	}

	if _, err := db.GetEnvironmentDefinition(ctx, w.ID, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing version = %v, want ErrNotFound", err)
	}
	if _, err := db.GetActiveEnvironmentDefinition(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get active with none = %v, want ErrNotFound", err)
	}
}

// TestEnvironmentDefinitionActivationDemotesPrevious covers the lifecycle
// transition and the single-active invariant: activating a version demotes
// the previously active one in the same transaction.
func TestEnvironmentDefinitionActivationDemotesPrevious(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)

	v1 := testEnvironmentDefinition(w.ID)
	v2 := testEnvironmentDefinition(w.ID)
	for _, d := range []*domain.EnvironmentDefinition{v1, v2} {
		if err := db.SaveEnvironmentDefinition(ctx, d); err != nil {
			t.Fatalf("SaveEnvironmentDefinition: %v", err)
		}
	}

	if err := db.SetEnvironmentStatus(ctx, w.ID, v1.Version, domain.EnvironmentActive, ""); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	active, err := db.GetActiveEnvironmentDefinition(ctx, w.ID)
	if err != nil || active.Version != 1 {
		t.Fatalf("active = %+v (err %v), want version 1", active, err)
	}

	if err := db.SetEnvironmentStatus(ctx, w.ID, v2.Version, domain.EnvironmentActive, ""); err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	active, err = db.GetActiveEnvironmentDefinition(ctx, w.ID)
	if err != nil || active.Version != 2 {
		t.Fatalf("active after second activation = %+v (err %v), want version 2", active, err)
	}
	demoted, err := db.GetEnvironmentDefinition(ctx, w.ID, 1)
	if err != nil {
		t.Fatalf("get demoted: %v", err)
	}
	if demoted.Status != domain.EnvironmentSaved {
		t.Fatalf("demoted status = %q, want %q", demoted.Status, domain.EnvironmentSaved)
	}
	if !demoted.UpdatedAt.After(v1.UpdatedAt) {
		t.Fatalf("demotion did not touch updated_at: %v", demoted.UpdatedAt)
	}

	// A failure carries its detail; a later transition clears it.
	if err := db.SetEnvironmentStatus(ctx, w.ID, 1, domain.EnvironmentFailed, "ripgrep: want 14.1.0, got 13.0.0"); err != nil {
		t.Fatalf("fail v1: %v", err)
	}
	failed, err := db.GetEnvironmentDefinition(ctx, w.ID, 1)
	if err != nil || failed.Status != domain.EnvironmentFailed || failed.FailureDetail == "" {
		t.Fatalf("failed row = %+v (err %v), want failed status with detail", failed, err)
	}
	if err := db.SetEnvironmentStatus(ctx, w.ID, 1, domain.EnvironmentBuilding, ""); err != nil {
		t.Fatalf("rebuild v1: %v", err)
	}
	rebuilt, err := db.GetEnvironmentDefinition(ctx, w.ID, 1)
	if err != nil || rebuilt.Status != domain.EnvironmentBuilding || rebuilt.FailureDetail != "" {
		t.Fatalf("rebuilt row = %+v (err %v), want building with no detail", rebuilt, err)
	}

	// Guard rails: unknown status and missing version are rejected.
	if err := db.SetEnvironmentStatus(ctx, w.ID, 2, "exploded", ""); err == nil {
		t.Fatal("unknown status accepted, want error")
	}
	if err := db.SetEnvironmentStatus(ctx, w.ID, 99, domain.EnvironmentActive, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transition of missing version = %v, want ErrNotFound", err)
	}
	// The failed activation must not have demoted the active version.
	if active, err = db.GetActiveEnvironmentDefinition(ctx, w.ID); err != nil || active.Version != 2 {
		t.Fatalf("active after failed activation = %+v (err %v), want version 2", active, err)
	}
}

// TestEnvironmentDefinitionListNewestFirst covers list ordering.
func TestEnvironmentDefinitionListNewestFirst(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)

	for i := 0; i < 3; i++ {
		if err := db.SaveEnvironmentDefinition(ctx, testEnvironmentDefinition(w.ID)); err != nil {
			t.Fatalf("SaveEnvironmentDefinition: %v", err)
		}
	}
	list, err := db.ListEnvironmentDefinitions(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListEnvironmentDefinitions: %v", err)
	}
	if len(list) != 3 || list[0].Version != 3 || list[1].Version != 2 || list[2].Version != 1 {
		t.Fatalf("list = %+v, want versions 3, 2, 1", list)
	}

	empty, err := db.ListEnvironmentDefinitions(ctx, "ws_missing")
	if err != nil || len(empty) != 0 {
		t.Fatalf("list for missing workspace = %+v (err %v), want empty", empty, err)
	}
}

// TestEnvironmentDefinitionWorkspaceDeleteCascades covers the cascade:
// deleting a workspace removes its definitions.
func TestEnvironmentDefinitionWorkspaceDeleteCascades(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)

	d := testEnvironmentDefinition(w.ID)
	if err := db.SaveEnvironmentDefinition(ctx, d); err != nil {
		t.Fatalf("SaveEnvironmentDefinition: %v", err)
	}
	if err := db.DeleteWorkspace(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if _, err := db.GetEnvironmentDefinition(ctx, w.ID, d.Version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after workspace delete = %v, want ErrNotFound", err)
	}
	list, err := db.ListEnvironmentDefinitions(ctx, w.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("list after workspace delete = %+v (err %v), want empty", list, err)
	}
}
