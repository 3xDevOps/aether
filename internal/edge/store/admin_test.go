package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

var testNow = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

const (
	serverA = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	serverB = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() }) //nolint:errcheck // test cleanup
	return s
}

func owner() edgeproto.Account {
	return edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1", Login: "Owner", Email: "owner@example.test"}
}

// addDevice gives account id a device through the device flow.
func addDevice(t *testing.T, s *Store, accountID int64, deviceID, tokenHash string) {
	t.Helper()
	ctx := context.Background()
	auth := DeviceAuth{CodeHash: "code-" + deviceID, UserCodeHash: "user-" + deviceID, Label: "laptop",
		Key: "ssh-ed25519 AAAA", CreatedAt: testNow, ExpiresAt: testNow.Add(time.Hour)}
	if err := s.CreateDeviceAuth(ctx, auth); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideDeviceAuth(ctx, auth.UserCodeHash, accountID, true, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemDeviceAuth(ctx, auth.CodeHash, Device{ID: deviceID, TokenHash: tokenHash}, testNow); err != nil {
		t.Fatal(err)
	}
}

// claim signs a in and records it as the owner of server id.
func claim(t *testing.T, s *Store, id, name string, a edgeproto.Account) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.SignIn(ctx, a, testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordClaim(ctx, id, name, edgeproto.PolicyAccount, edgeproto.AccountPrincipal(a), testNow); err != nil {
		t.Fatal(err)
	}
}

func TestRecordClaim(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	o := edgeproto.AccountPrincipal(owner())
	if err := s.RecordClaim(ctx, serverA, "alpha", edgeproto.PolicyAccount, o, testNow); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("claim naming an account the edge does not hold: %v", err)
	}
	if _, err := s.SignIn(ctx, owner(), testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.BlockServer(ctx, serverA, testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordClaim(ctx, serverA, "alpha", edgeproto.PolicyAccount, o, testNow); !errors.Is(err, ErrServerBlocked) {
		t.Fatalf("claim of a blocked server: %v", err)
	}
	if _, err := s.Server(ctx, serverA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused claim recorded the server: %v", err)
	}
	if err := s.UnblockServer(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordClaim(ctx, serverA, "alpha", edgeproto.PolicyAccount, o, testNow); err != nil {
		t.Fatal(err)
	}
	srv, err := s.Server(ctx, serverA)
	if err != nil || srv.Owner == nil || srv.Owner.Subject != "1" || !edgeproto.ValidAccountID(srv.Owner.ID) ||
		srv.Name != "alpha" || srv.Kind != edgeproto.ServerSelfHosted || srv.AccessPolicy != edgeproto.PolicyAccount {
		t.Fatalf("claimed server %+v, %v", srv, err)
	}

	other := edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "2"}
	if _, err = s.SignIn(ctx, other, testNow); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordClaim(ctx, serverA, "alpha", edgeproto.PolicyAccount, edgeproto.AccountPrincipal(other), testNow); !errors.Is(err, ErrHasOwner) {
		t.Fatalf("second claim of an owned server: %v", err)
	}
	if err = s.DropOwner(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	if owned, oerr := s.ServerOwned(ctx, serverA); oerr != nil || owned {
		t.Fatalf("ownerless server owned = %v, %v", owned, oerr)
	}
	if err = s.RecordClaim(ctx, serverA, "alpha", edgeproto.PolicyApprovedDevices, edgeproto.AccountPrincipal(other), testNow); err != nil {
		t.Fatalf("claim of an ownerless server: %v", err)
	}
	if srv, err = s.Server(ctx, serverA); err != nil || srv.Owner.Subject != "2" || srv.AccessPolicy != edgeproto.PolicyApprovedDevices {
		t.Fatalf("reclaimed server %+v, %v", srv, err)
	}

	stranger := edgeproto.Principal{Type: edgeproto.PrincipalAccount, Provider: edgeproto.ProviderGitHub, Subject: "404"}
	if err = s.TransferOwner(ctx, serverA, stranger); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("transfer to an account the edge does not hold: %v", err)
	}
	if err = s.TransferOwner(ctx, serverB, o); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transfer of an unclaimed server: %v", err)
	}
	if err = s.BlockAccount(ctx, o.Provider, o.Subject, testNow); err != nil {
		t.Fatal(err)
	}
	if err = s.TransferOwner(ctx, serverA, o); !errors.Is(err, ErrAccountBlocked) {
		t.Fatalf("transfer to a blocked account: %v", err)
	}
	if err = s.UnblockAccount(ctx, o.Provider, o.Subject); err != nil {
		t.Fatal(err)
	}
	if err = s.TransferOwner(ctx, serverA, o); err != nil {
		t.Fatal(err)
	}
	if srv, err = s.Server(ctx, serverA); err != nil || srv.Owner.Subject != "1" {
		t.Fatalf("transferred server %+v, %v", srv, err)
	}
	if err = s.DropOwner(ctx, serverB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("drop owner of an unclaimed server: %v", err)
	}
}

func TestAccountIDIsAssignedOnce(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	first, err := s.SignIn(ctx, owner(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateSession(ctx, "session-1", "hash-1", first, testNow, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	renamed := owner()
	renamed.Login, renamed.Email = "renamed", "new@example.test"
	if _, err = s.SignIn(ctx, renamed, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	sess, err := s.UseSession(ctx, "hash-1", testNow, testNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !edgeproto.ValidAccountID(sess.Account.ID) || sess.Account.Login != "renamed" {
		t.Fatalf("account %+v", sess.Account)
	}
	id := sess.Account.ID
	if _, err = s.DeleteAccount(ctx, renamed.Provider, renamed.Subject, testNow); err != nil {
		t.Fatal(err)
	}
	again, err := s.SignIn(ctx, renamed, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateSession(ctx, "session-2", "hash-2", again, testNow, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if sess, err = s.UseSession(ctx, "hash-2", testNow, testNow.Add(time.Hour)); err != nil || sess.Account.ID == id {
		t.Fatalf("a deleted account signing in again kept its id %s: %+v, %v", id, sess.Account, err)
	}
}

func TestBlockServer(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	claim(t, s, serverA, "alpha", owner())
	claim(t, s, serverB, "beta", owner())
	if err := s.BlockServer(ctx, serverA, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Server(ctx, serverA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blocked server still claimed: %v", err)
	}
	if blocked, err := s.ServerBlocked(ctx, serverA); err != nil || !blocked {
		t.Fatalf("ServerBlocked = %v, %v", blocked, err)
	}
	rows, err := s.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != serverB || rows[0].ClaimedAt.IsZero() || rows[0].Owner.Subject != "1" ||
		rows[1].ID != serverA || rows[1].BlockedAt.IsZero() || rows[1].Name != "" {
		t.Fatalf("servers %+v", rows)
	}
	if err := s.UnblockServer(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	if err := s.UnblockServer(ctx, serverA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unblock of a server that is not blocked: %v", err)
	}
}

func TestBlockAccount(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := owner()
	id, err := s.SignIn(ctx, a, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateSession(ctx, "session-1", "session-hash", id, testNow, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	addDevice(t, s, id, "device-1", "token-hash")
	stranger := edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "never-signed-in"}

	for _, b := range []edgeproto.Account{a, stranger} {
		if err = s.BlockAccount(ctx, b.Provider, b.Subject, testNow); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.UseSession(ctx, "session-hash", testNow, testNow.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("session of a blocked account: %v", err)
	}
	if _, err = s.UseDevice(ctx, "token-hash", testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("device token of a blocked account: %v", err)
	}
	for _, b := range []edgeproto.Account{a, stranger} {
		if _, err = s.SignIn(ctx, b, testNow); !errors.Is(err, ErrAccountBlocked) {
			t.Errorf("sign-in of blocked %s:%s: %v", b.Provider, b.Subject, err)
		}
	}
	rows, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Account.Subject != "1" || !rows[0].Blocked || rows[0].Devices != 0 ||
		rows[1].Account != stranger || !rows[1].Blocked || !rows[1].CreatedAt.IsZero() {
		t.Fatalf("accounts %+v", rows)
	}

	if err := s.UnblockAccount(ctx, a.Provider, a.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SignIn(ctx, a, testNow); err != nil {
		t.Fatalf("sign-in after unblock: %v", err)
	}
	if err := s.UnblockAccount(ctx, a.Provider, a.Subject); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unblock of an account that is not blocked: %v", err)
	}
}

func TestDeleteAccount(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := owner()
	id, err := s.SignIn(ctx, a, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateSession(ctx, "session-1", "session-hash", id, testNow, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	addDevice(t, s, id, "device-1", "token-hash")
	claim(t, s, serverA, "alpha", a)
	other := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2", Login: "other"}
	claim(t, s, serverB, "beta", other)
	serverC := "cccccccccccccccccccccccccc"
	claim(t, s, serverC, "gamma", other)
	kept := []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: "github", Subject: "2", Role: "admin"},
		{Kind: edgeproto.EntryInvitation, Provider: "github", Login: "owner", Role: "viewer", ExpiresAt: testNow.Add(time.Hour)},
		{Kind: edgeproto.EntryInvitation, Email: "Owner@Example.test", Role: "viewer", ExpiresAt: testNow.Add(time.Hour)},
	}
	if err = s.ReplaceDirectory(ctx, serverB, append(slices.Clone(kept),
		edgeproto.DirectoryEntry{Kind: edgeproto.EntryMember, Provider: "github", Subject: "1", Role: "collaborator"})); err != nil {
		t.Fatal(err)
	}
	if err = s.BlockAccount(ctx, a.Provider, a.Subject, testNow); err != nil {
		t.Fatal(err)
	}
	// The relay admitted the account to serverD, whose directory does not
	// name it yet, and to serverB, and the other account to serverC.
	serverD := "dddddddddddddddddddddddddd"
	claim(t, s, serverD, "delta", other)
	for _, id := range []string{serverD, serverB, serverD} {
		if err = s.RecordReach(ctx, id, a); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecordReach(ctx, serverC, other); err != nil {
		t.Fatal(err)
	}

	got, err := s.DeleteAccount(ctx, a.Provider, a.Subject, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account.Login != "Owner" || !slices.Equal(got.Owned, []string{serverA}) ||
		!slices.Equal(got.Member, []string{serverB}) || !slices.Equal(got.Reached, []string{serverB, serverD}) ||
		!slices.Equal(got.Notify, []string{serverA, serverB, serverD}) {
		t.Fatalf("deleted %+v", got)
	}
	var reach int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM account_reach`).Scan(&reach); err != nil || reach != 1 {
		t.Fatalf("admissions left after the deletion = %d, %v; want the other account's only", reach, err)
	}
	srv, err := s.Server(ctx, serverA)
	if err != nil || srv.Owner != nil {
		t.Errorf("owned server %+v, %v; want it kept, ownerless", srv, err)
	}
	var entries []edgeproto.DirectoryEntry
	rows, err := s.db.Query(`SELECT kind, provider, subject, login, email, role, expires_at FROM directory_entries
		WHERE server_id = ? ORDER BY rowid`, serverB)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var e edgeproto.DirectoryEntry
		var expires int64
		if err = rows.Scan(&e.Kind, &e.Provider, &e.Subject, &e.Login, &e.Email, &e.Role, &expires); err != nil {
			t.Fatal(err)
		}
		if expires != 0 {
			e.ExpiresAt = fromUnix(expires)
		}
		entries = append(entries, e)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(entries, kept) {
		t.Errorf("directory of %s after delete %+v, want %+v", serverB, entries, kept)
	}
	for _, sid := range []string{serverA, serverB, serverD} {
		owed, perr := s.PendingDeletions(ctx, sid)
		if perr != nil || len(owed) != 1 || owed[0] != (edgeproto.AccountDeleted{Provider: a.Provider, Subject: a.Subject}) {
			t.Errorf("owed to %s: %+v, %v", sid, owed, perr)
		}
	}
	if owed, perr := s.PendingDeletions(ctx, serverC); perr != nil || len(owed) != 0 {
		t.Errorf("owed to %s, which never named the account: %+v, %v", serverC, owed, perr)
	}
	var left int
	for _, table := range []string{"web_sessions", "devices", "device_authorizations"} {
		var n int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		left += n
	}
	if left != 0 {
		t.Errorf("%d session or device rows left", left)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The block outlives the account, so the person cannot sign back in.
	if len(accounts) != 2 || accounts[1].Account.Subject != "1" || !accounts[1].Blocked || accounts[1].Account.Login != "" {
		t.Errorf("accounts after delete %+v", accounts)
	}
	if _, err := s.DeleteAccount(ctx, a.Provider, a.Subject, testNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestPendingDeletions(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	// A server owed its limit, subjects 0 to 999, is owed one more.
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i + 1 < ?)
		INSERT INTO pending_account_deletions (server_id, provider, subject, created_at)
		SELECT ?, 'github', CAST(i AS TEXT), ? + i FROM n`, maxPendingDeletions, serverA, unix(testNow)); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	last := strconv.Itoa(maxPendingDeletions)
	if err = oweDeletion(ctx, tx, serverA, "github", last, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	owed, err := s.PendingDeletions(ctx, serverA)
	if err != nil {
		t.Fatal(err)
	}
	if len(owed) != maxPendingDeletions || owed[0].Subject != "1" || owed[len(owed)-1].Subject != last {
		t.Fatalf("owed %d deletions from %+v to %+v; want the newest %d", len(owed), owed[0], owed[len(owed)-1], maxPendingDeletions)
	}
	if err := s.DeletionDelivered(ctx, serverA, owed[0]); err != nil {
		t.Fatal(err)
	}
	if again, err := s.PendingDeletions(ctx, serverA); err != nil || len(again) != maxPendingDeletions-1 || again[0] == owed[0] {
		t.Fatalf("after delivering %+v: %d owed, %v", owed[0], len(again), err)
	}
}

// TestMigrationOnPopulatedDatabase upgrades a database written by the
// previous schema: accounts get distinct ids, and servers keep their
// owners and directories.
func TestMigrationOnPopulatedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate(db, migrations[:6]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO accounts (id, provider, subject, email, login, name, created_at, identity_at)
			VALUES (1, 'github', '1', 'owner@example.test', 'owner', 'Owner', 1, 1),
			       (2, 'google', '2', '', '', 'Other', 1, 1)`,
		`INSERT INTO servers (id, name, owner_id, claimed_at) VALUES ('` + serverA + `', 'alpha', 1, 5)`,
		`INSERT INTO directory_entries (server_id, kind, provider, subject, login, email, role, expires_at)
			VALUES ('` + serverA + `', 'member', 'google', '2', '', '', 'collaborator', 0)`,
		`INSERT INTO web_sessions (id, token_hash, account_id, created_at, expires_at) VALUES ('s', 'h', 2, 1, 9e18)`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() }) //nolint:errcheck // test cleanup
	ctx := context.Background()
	srv, err := s.Server(ctx, serverA)
	if err != nil || srv.Name != "alpha" || srv.Owner == nil || srv.Owner.Login != "owner" ||
		srv.Kind != edgeproto.ServerSelfHosted || srv.AccessPolicy != edgeproto.PolicyApprovedDevices || !srv.ClaimedAt.Equal(fromUnix(5)) {
		t.Fatalf("migrated server %+v, %v", srv, err)
	}
	sess, err := s.UseSession(ctx, "h", testNow, testNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !edgeproto.ValidAccountID(sess.Account.ID) || sess.Account.ID == srv.Owner.ID || !edgeproto.ValidAccountID(srv.Owner.ID) {
		t.Fatalf("account ids %q and %q", sess.Account.ID, srv.Owner.ID)
	}
	acc, err := s.ServerAccess(ctx, serverA, edgeproto.Account{Provider: "google", Subject: "2"})
	if err != nil || len(acc.Entries) != 1 || acc.Entries[0].Role != "collaborator" || acc.Owner {
		t.Fatalf("migrated directory %+v, %v", acc, err)
	}
	// The rebuilt directory still belongs to its server.
	if err := s.DeleteServer(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM directory_entries`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d directory entries outlive their server, %v", left, err)
	}
}

// TestExpiryPrunesUseAnIndex checks the deletes that run before every new
// session and device authorization: unauthenticated requests start the
// latter, so the delete must not scan its table.
func TestExpiryPrunesUseAnIndex(t *testing.T) {
	s := openStore(t)
	for _, table := range []string{"web_sessions", "device_authorizations"} {
		rows, err := s.db.Query(`EXPLAIN QUERY PLAN DELETE FROM `+table+` WHERE expires_at <= ?`, 0)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(plan, "; "); !strings.Contains(got, "USING") || !strings.Contains(got, "INDEX") {
			t.Errorf("expiry delete on %s: plan %q scans the table", table, got)
		}
	}
}
