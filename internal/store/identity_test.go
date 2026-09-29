package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TestEdgeIdentityMigrationKeepsMembersAndCascadeChildren upgrades a
// populated database from the version before edge identities. The members
// rebuild must keep every row of every table, including the tables that
// reference members ON DELETE CASCADE, which a DROP TABLE with foreign
// keys on would have emptied.
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
	before := rowCounts(t, raw)
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
	after := rowCounts(t, db.db)
	for table, n := range before {
		if after[table] != n {
			t.Errorf("table %s has %d rows after the migration, %d before", table, after[table], n)
		}
	}
	var fk int
	if err := db.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys after migration = %d, %v; want 1", fk, err)
	}
	if err := db.CreateMember(ctx, &domain.Member{DisplayName: "x", Color: "#000000", Role: domain.RoleViewer}); err == nil {
		t.Fatal("CreateMember without key or tailnet login succeeded")
	}
}

// rowCounts counts the rows of every table in db except the migration
// bookkeeping.
func rowCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
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

// edgeMemberWithIdentity creates a member bound to (github, subject).
func edgeMemberWithIdentity(t *testing.T, db *DB, subject string) *domain.Member {
	t.Helper()
	admin := mustCreateMember(t, db)
	m, err := db.AcceptInvitation(context.Background(),
		mustInvite(t, db, admin.ID, domain.Invitation{Provider: "github", Login: "u" + subject, Role: domain.RoleCollaborator}).ID,
		&domain.Identity{Provider: "github", Subject: subject, Login: "u" + subject}, edgeMember("u"+subject), time.Now())
	if err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}
	return m
}

func newDevice(t *testing.T, m *domain.Member, subject string, status domain.DeviceStatus) *domain.Device {
	t.Helper()
	return &domain.Device{Member: m.ID, Provider: "github", Subject: subject, Credential: testKey(t, ""),
		Label: "laptop", Status: status}
}

// A device's status is what the caller registers it with: the store has
// no first-device exception.
func TestDeviceStatusIsTheCallers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	m := edgeMemberWithIdentity(t, db, "42")

	pending := newDevice(t, m, "42", domain.DevicePending)
	if err := db.RegisterDevice(ctx, pending); err != nil {
		t.Fatalf("RegisterDevice first: %v", err)
	}
	if pending.Status != domain.DevicePending || pending.ApprovalCode != ApprovalCode(pending.Credential) {
		t.Fatalf("a member's first device = %+v, want pending with its key's code", pending)
	}
	registered := newDevice(t, m, "42", domain.DeviceRegistered)
	if err := db.RegisterDevice(ctx, registered); err != nil || registered.ApprovalCode == "" {
		t.Fatalf("registered device = %+v, %v; want an approval code", registered, err)
	}
	typed := registered.ApprovalCode[:4] + registered.ApprovalCode[5:]
	got, err := db.GetDeviceByApprovalCode(ctx, " "+strings.ToLower(typed)+" ")
	if err != nil || got.ID != registered.ID {
		t.Fatalf("GetDeviceByApprovalCode(%q) = %+v, %v", typed, got, err)
	}
	if err = db.ApproveDevice(ctx, registered.ID, m.ID); err != nil {
		t.Fatalf("ApproveDevice of a registered device: %v", err)
	}
	if err = db.ApproveDevice(ctx, registered.ID, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second ApproveDevice = %v, want ErrNotFound", err)
	}
	if _, err = db.GetDeviceByApprovalCode(ctx, registered.ApprovalCode); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approval code still resolves after approval: %v", err)
	}
	if err = db.RevokeDevice(ctx, pending.ID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	if err = db.ApproveDevice(ctx, pending.ID, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ApproveDevice of a revoked device = %v, want ErrNotFound", err)
	}
	dup := newDevice(t, m, "42", domain.DeviceApproved)
	dup.Credential = registered.Credential
	if err = db.RegisterDevice(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate credential = %v, want ErrConflict", err)
	}
	now := time.Now().UTC()
	if err = db.TouchDevice(ctx, registered.ID, now); err != nil {
		t.Fatalf("TouchDevice: %v", err)
	}
	byKey, err := db.GetDeviceByCredential(ctx, registered.Credential)
	if err != nil || byKey.LastSeenAt == nil || !byKey.LastSeenAt.Equal(now) || byKey.Subject != "42" {
		t.Fatalf("GetDeviceByCredential = %+v, %v", byKey, err)
	}
}

// A device belongs to one identity of its member; an identity of another
// member, or none, registers nothing.
func TestDeviceNeedsTheMembersIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	m := edgeMemberWithIdentity(t, db, "42")
	other := edgeMemberWithIdentity(t, db, "43")
	for name, subject := range map[string]string{"another member's identity": "43", "no identity": "44"} {
		dev := newDevice(t, m, subject, domain.DevicePending)
		if err := db.RegisterDevice(ctx, dev); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: RegisterDevice = %v, want ErrNotFound", name, err)
		}
	}
	if devs, _ := db.ListDevices(ctx, ""); len(devs) != 0 {
		t.Fatalf("refused registrations left devices: %+v", devs)
	}
	if err := db.RegisterDevice(ctx, newDevice(t, other, "43", domain.DeviceRegistered)); err != nil {
		t.Fatalf("RegisterDevice through the member's own identity: %v", err)
	}
}

// Two keys can derive one approval code. Such a code approves neither, so
// a key ground to match a waiting device's code cannot take its approval.
func TestSharedApprovalCodeApprovesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	m := edgeMemberWithIdentity(t, db, "42")
	first, second := newDevice(t, m, "42", domain.DevicePending), newDevice(t, m, "42", domain.DevicePending)
	for _, dev := range []*domain.Device{first, second} {
		if err := db.RegisterDevice(ctx, dev); err != nil {
			t.Fatalf("RegisterDevice: %v", err)
		}
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE member_devices SET approval_code = ? WHERE id = ?`,
		first.ApprovalCode, second.ID); err != nil {
		t.Fatalf("collide codes: %v", err)
	}
	if got, err := db.GetDeviceByApprovalCode(ctx, first.ApprovalCode); !errors.Is(err, ErrConflict) {
		t.Fatalf("shared code = %+v, %v; want ErrConflict", got, err)
	}
}

// Removing an identity takes its devices and nothing else of the member.
func TestRemoveIdentityKeepsTheMember(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	m := edgeMemberWithIdentity(t, db, "42")
	if _, err := db.AcceptInvitation(ctx,
		mustInvite(t, db, m.ID, domain.Invitation{Provider: "google", Email: "u@example.com", Member: m.ID}).ID,
		&domain.Identity{Provider: "google", Subject: "g1", Email: "u@example.com"}, nil, time.Now()); err != nil {
		t.Fatalf("link google: %v", err)
	}
	gone := newDevice(t, m, "42", domain.DeviceApproved)
	kept := &domain.Device{Member: m.ID, Provider: "google", Subject: "g1", Credential: testKey(t, ""), Label: "phone",
		Status: domain.DeviceApproved}
	for _, dev := range []*domain.Device{gone, kept} {
		if err := db.RegisterDevice(ctx, dev); err != nil {
			t.Fatalf("RegisterDevice: %v", err)
		}
	}
	member, devs, err := db.RemoveIdentity(ctx, "github", "42")
	if err != nil || member != m.ID || len(devs) != 1 || devs[0] != gone.ID {
		t.Fatalf("RemoveIdentity = %s, %v, %v; want %s and its one device", member, devs, err, m.ID)
	}
	if after, err := db.GetMember(ctx, m.ID); err != nil || after.Role != m.Role {
		t.Fatalf("member after RemoveIdentity = %+v, %v; want unchanged", after, err)
	}
	if _, err := db.GetDevice(ctx, gone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the identity's device survived: %v", err)
	}
	if _, err := db.GetDevice(ctx, kept.ID); err != nil {
		t.Fatalf("the other identity's device went too: %v", err)
	}
	if _, _, err := db.RemoveIdentity(ctx, "github", "42"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second RemoveIdentity = %v, want ErrNotFound", err)
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
	for range 2 {
		if err := db.RegisterDevice(ctx, &domain.Device{Member: m.ID, Provider: "github", Subject: "42",
			Credential: testKey(t, ""), Label: "x", Status: domain.DevicePending}); err != nil {
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
