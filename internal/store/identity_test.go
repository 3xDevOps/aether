package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TestEdgeIdentityMigrationKeepsMembersAndCascadeChildren upgrades a
// database from the version before edge identities. The members rebuild
// must keep every member row and every row of the tables that reference
// members ON DELETE CASCADE, which a DROP TABLE with foreign keys on would
// have emptied.
func TestEdgeIdentityMigrationKeepsMembersAndCascadeChildren(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, len(migrations)-1)
	key := testKey(t, "")
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, tailnet_login, pending, color, role, created_at, image, git_name, git_email)
			VALUES ('m1', 'Ada', ?, '', 0, '#e6194b', 'admin', 1, 'img:1', 'Ada L', 'ada@example.com'),
			       ('m2', 'bob', '', 'bob@example.com', 1, '#3cb44b', 'collaborator', 2, '', '', '');
		INSERT INTO account_shares (owner_member_id, grantee_member_id, created_at) VALUES ('m1', 'm2', 3);
		INSERT INTO harness_definitions (member_id, name, definition, created_at, updated_at)
			VALUES ('m1', 'mine', '{}', 4, 4);
	`, key); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	m1, err := db.GetMemberByPublicKey(ctx, key)
	if err != nil {
		t.Fatalf("GetMemberByPublicKey: %v", err)
	}
	if m1.ID != "m1" || m1.Role != domain.RoleAdmin || m1.Image != "img:1" || m1.GitEmail != "ada@example.com" {
		t.Fatalf("m1 after migration = %+v", m1)
	}
	m2, err := db.GetMemberByTailnetLogin(ctx, "bob@example.com")
	if err != nil || m2.ID != "m2" || !m2.Pending {
		t.Fatalf("m2 after migration = %+v, %v", m2, err)
	}
	if ok, shareErr := db.AccountSharedWith(ctx, "m1", "m2"); shareErr != nil || !ok {
		t.Fatalf("account share after migration = %v, %v; want kept", ok, shareErr)
	}
	defs, err := db.ListHarnessDefinitions(ctx, "m1")
	if err != nil || len(defs) != 1 {
		t.Fatalf("harness definitions after migration = %d, %v; want 1", len(defs), err)
	}
	var fk int
	if err := db.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys after migration = %d, %v; want 1", fk, err)
	}
	if err := db.CreateMember(ctx, &domain.Member{DisplayName: "x", Color: "#000000", Role: domain.RoleViewer}); err == nil {
		t.Fatal("CreateMember without key or tailnet login succeeded")
	}
}

func edgeMember(name string) *domain.Member {
	return &domain.Member{DisplayName: name, Color: "#4363d8"}
}

func TestClaimMemberOnlyOnEmptyServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	owner := edgeMember("owner")
	owner.Role = domain.RoleAdmin
	if err := db.ClaimMember(ctx, owner, &domain.Identity{Provider: "github", Subject: "1", Login: "octo"}); err != nil {
		t.Fatalf("ClaimMember: %v", err)
	}
	got, err := db.GetMemberByIdentity(ctx, "github", "1")
	if err != nil || got.ID != owner.ID || got.PublicKey != "" || got.TailnetLogin != "" {
		t.Fatalf("GetMemberByIdentity = %+v, %v", got, err)
	}
	// An edge-only member stays updatable: a role change keeps it valid.
	got.Role = domain.RoleCollaborator
	if err = db.UpdateMember(ctx, got); err != nil {
		t.Fatalf("UpdateMember of an edge-only member: %v", err)
	}
	second := edgeMember("second")
	second.Role = domain.RoleAdmin
	err = db.ClaimMember(ctx, second, &domain.Identity{Provider: "github", Subject: "2"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second ClaimMember = %v, want ErrConflict", err)
	}
	if _, err := db.GetMemberByIdentity(ctx, "github", "2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused claim left an identity: %v", err)
	}
}

func mustInvite(t *testing.T, db *DB, by domain.MemberID, inv domain.Invitation) *domain.Invitation {
	t.Helper()
	inv.CreatedBy = by
	if inv.ExpiresAt.IsZero() {
		inv.ExpiresAt = time.Now().Add(time.Hour)
	}
	if err := db.CreateInvitation(context.Background(), &inv); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	return &inv
}

func TestAcceptInvitationCreatesOneMemberUnderConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	admin := mustCreateMember(t, db)
	inv := mustInvite(t, db, admin.ID, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleViewer})

	const racers = 8
	ids := make([]domain.MemberID, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			m, err := db.AcceptInvitation(ctx, inv.ID,
				&domain.Identity{Provider: "github", Subject: "42", Login: "octo"}, edgeMember("octo"), time.Now())
			errs[i] = err
			if err == nil {
				ids[i] = m.ID
			}
		})
	}
	wg.Wait()
	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("accept %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("accepts returned members %s and %s, want one", ids[0], ids[i])
		}
	}
	members, err := db.ListMembers(ctx)
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %d, %v; want admin and one invitee", len(members), err)
	}
	m, err := db.GetMember(ctx, ids[0])
	if err != nil || m.Role != domain.RoleViewer || m.Pending {
		t.Fatalf("invitee = %+v, %v; want an approved viewer", m, err)
	}
	if list, _ := db.ListInvitations(ctx); len(list) != 0 {
		t.Fatalf("invitation still listed after acceptance: %+v", list)
	}

	// A consumed invitation binds no second account.
	_, err = db.AcceptInvitation(ctx, inv.ID,
		&domain.Identity{Provider: "github", Subject: "43", Login: "octo"}, edgeMember("other"), time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("second account on a consumed invitation = %v, want ErrNotFound", err)
	}
}

func TestAcceptInvitationRefusesExpired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	admin := mustCreateMember(t, db)
	inv := mustInvite(t, db, admin.ID, domain.Invitation{Email: "a@example.com", Role: domain.RoleCollaborator,
		ExpiresAt: time.Now().Add(time.Minute)})
	_, err := db.AcceptInvitation(ctx, inv.ID,
		&domain.Identity{Provider: "google", Subject: "g1", Email: "a@example.com"}, edgeMember("a"), time.Now().Add(time.Hour))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired invitation = %v, want ErrNotFound", err)
	}
	if _, err := db.GetMemberByIdentity(ctx, "google", "g1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired invitation bound an identity: %v", err)
	}
}

func TestAcceptLinkInvitationBindsExistingMember(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	admin := mustCreateMember(t, db)
	inv := mustInvite(t, db, admin.ID, domain.Invitation{Provider: "github", Login: "ada", Member: admin.ID})
	m, err := db.AcceptInvitation(ctx, inv.ID,
		&domain.Identity{Provider: "github", Subject: "7", Login: "ada"}, edgeMember("ignored"), time.Now())
	if err != nil || m.ID != admin.ID || m.Role != admin.Role {
		t.Fatalf("link accept = %+v, %v; want the linking member unchanged", m, err)
	}
	if members, _ := db.ListMembers(ctx); len(members) != 1 {
		t.Fatalf("link created a member: %d members", len(members))
	}
}

func TestDevicesFirstApprovedThenPending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	m := mustCreateMember(t, db)

	first := &domain.Device{Member: m.ID, Kind: domain.DeviceSSH, Credential: testKey(t, ""), Label: "laptop"}
	if err := db.RegisterDevice(ctx, first, true); err != nil {
		t.Fatalf("RegisterDevice first: %v", err)
	}
	if first.Status != domain.DeviceApproved || first.ApprovalCode != "" {
		t.Fatalf("first device = %+v, want approved", first)
	}
	second := &domain.Device{Member: m.ID, Kind: domain.DeviceSSH, Credential: testKey(t, ""), Label: "phone"}
	if err := db.RegisterDevice(ctx, second, true); err != nil {
		t.Fatalf("RegisterDevice second: %v", err)
	}
	if second.Status != domain.DevicePending || len(second.ApprovalCode) != 9 {
		t.Fatalf("second device = %+v, want pending with a code", second)
	}
	typed := second.ApprovalCode[:4] + second.ApprovalCode[5:]
	got, err := db.GetDeviceByApprovalCode(ctx, " "+strings.ToLower(typed)+" ")
	if err != nil || got.ID != second.ID {
		t.Fatalf("GetDeviceByApprovalCode(%q) = %+v, %v", typed, got, err)
	}
	if err = db.ApproveDevice(ctx, second.ID, m.ID); err != nil {
		t.Fatalf("ApproveDevice: %v", err)
	}
	if err = db.ApproveDevice(ctx, second.ID, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second ApproveDevice = %v, want ErrNotFound", err)
	}
	if _, err = db.GetDeviceByApprovalCode(ctx, second.ApprovalCode); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approval code still resolves after approval: %v", err)
	}
	if err = db.RevokeDevice(ctx, first.ID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	// Revoking every device does not reopen the first-device window.
	third := &domain.Device{Member: m.ID, Kind: domain.DeviceBrowser, Credential: "hash-3", Label: "browser"}
	if err = db.RegisterDevice(ctx, third, true); err != nil || third.Status != domain.DevicePending {
		t.Fatalf("device after a revocation = %+v, %v; want pending", third, err)
	}
	fourth := &domain.Device{Member: m.ID, Kind: domain.DeviceBrowser, Credential: "hash-4", Label: "browser"}
	if err = db.RegisterDevice(ctx, fourth, false); err != nil || fourth.Status != domain.DeviceApproved {
		t.Fatalf("device without approval required = %+v, %v; want approved", fourth, err)
	}
	dup := &domain.Device{Member: m.ID, Kind: domain.DeviceBrowser, Credential: "hash-4", Label: "again"}
	if err = db.RegisterDevice(ctx, dup, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate credential = %v, want ErrConflict", err)
	}
	now := time.Now().UTC()
	if err = db.TouchDevice(ctx, fourth.ID, now); err != nil {
		t.Fatalf("TouchDevice: %v", err)
	}
	byHash, err := db.GetDeviceByCredential(ctx, "hash-4")
	if err != nil || byHash.LastSeenAt == nil || !byHash.LastSeenAt.Equal(now) {
		t.Fatalf("GetDeviceByCredential = %+v, %v", byHash, err)
	}
}

func TestDeleteMemberRemovesIdentitiesDevicesAndInvitations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	admin := mustCreateMember(t, db)
	m, err := db.AcceptInvitation(ctx,
		mustInvite(t, db, admin.ID, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin}).ID,
		&domain.Identity{Provider: "github", Subject: "42", Login: "octo"}, edgeMember("octo"), time.Now())
	if err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}
	for _, kind := range []domain.DeviceKind{domain.DeviceSSH, domain.DeviceBrowser} {
		if err := db.RegisterDevice(ctx, &domain.Device{Member: m.ID, Kind: kind, Credential: string(kind) + "-cred", Label: "x"}, true); err != nil {
			t.Fatalf("RegisterDevice: %v", err)
		}
	}
	mustInvite(t, db, m.ID, domain.Invitation{Email: "c@example.com", Role: domain.RoleViewer})
	mustInvite(t, db, admin.ID, domain.Invitation{Email: "m@example.com", Member: m.ID})

	if err := db.DeleteMember(ctx, m.ID); err != nil {
		t.Fatalf("DeleteMember: %v", err)
	}
	if _, err := db.GetMemberByIdentity(ctx, "github", "42"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("identity survived member removal: %v", err)
	}
	if devs, _ := db.ListDevices(ctx, ""); len(devs) != 0 {
		t.Fatalf("devices survived member removal: %+v", devs)
	}
	if invs, _ := db.ListInvitations(ctx); len(invs) != 0 {
		t.Fatalf("invitations survived member removal: %+v", invs)
	}
}
