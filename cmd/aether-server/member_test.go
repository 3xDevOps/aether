package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// member role writes the role to the store, never demotes the last
// approved super admin, a pending one not counting, and keeps a workspace
// owner an admin.
func TestMemberRoleCommand(t *testing.T) {
	t.Parallel()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	super := &domain.Member{DisplayName: "Sam", TailnetLogin: "sam@", Color: "#e6194b", Role: domain.RoleSuperAdmin}
	owner := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@", Color: "#3cb44b", Role: domain.RoleAdmin}
	pending := &domain.Member{DisplayName: "Pat", TailnetLogin: "pat@", Color: "#ffe119", Role: domain.RoleSuperAdmin, Pending: true}
	for _, m := range []*domain.Member{super, owner, pending} {
		if err = db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	w := &domain.Workspace{Name: "web", OwnerID: owner.ID}
	if err = db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	role := func(id domain.MemberID, r domain.Role) (string, error) {
		var out bytes.Buffer
		rerr := memberRole(ctx, &out, db, id, r)
		return out.String(), rerr
	}
	if _, err = role(super.ID, domain.RoleAdmin); err == nil || !strings.Contains(err.Error(), "refusing to demote the last approved super admin") {
		t.Fatalf("demote the last super admin = %v", err)
	}
	if _, err = role(owner.ID, domain.RoleViewer); err == nil || !strings.Contains(err.Error(), string(w.ID)) {
		t.Fatalf("demote a workspace owner = %v, want refused naming %s", err, w.ID)
	}
	if _, err = role(owner.ID, "owner"); err == nil || !strings.Contains(err.Error(), "unknown role") {
		t.Fatalf("unknown role = %v", err)
	}
	out, err := role(owner.ID, domain.RoleSuperAdmin)
	if err != nil || !strings.Contains(out, "is super_admin, was admin") {
		t.Fatalf("promote = %q, %v", out, err)
	}
	if _, err := role(super.ID, domain.RoleCollaborator); err != nil {
		t.Fatalf("demote a super admin beside another: %v", err)
	}
	if m, err := db.GetMember(ctx, super.ID); err != nil || m.Role != domain.RoleCollaborator {
		t.Fatalf("demoted member = %+v, %v", m, err)
	}
	if _, err := role("mem-nobody", domain.RoleViewer); err == nil || !strings.Contains(err.Error(), "no member mem-nobody") {
		t.Fatalf("unknown member = %v", err)
	}
}
