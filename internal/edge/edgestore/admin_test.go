package edgestore

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
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

func TestClaimServerRecordsNothingWhenRefused(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.BlockServer(ctx, serverA, testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimServer(ctx, serverA, "alpha", owner(), testNow); !errors.Is(err, ErrServerBlocked) {
		t.Fatalf("claim of a blocked server: %v", err)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("a refused claim created accounts %+v", accounts)
	}

	if err = s.UnblockServer(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	if err = s.ClaimServer(ctx, serverA, "alpha", owner(), testNow); err != nil {
		t.Fatal(err)
	}
	srv, err := s.Server(ctx, serverA)
	if err != nil || srv.Owner.Subject != "1" || srv.Name != "alpha" {
		t.Fatalf("claimed server %+v, %v", srv, err)
	}
}

func TestBlockServer(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.ClaimServer(ctx, serverA, "alpha", owner(), testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimServer(ctx, serverB, "beta", owner(), testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.BlockServer(ctx, serverA, testNow); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.Claimed(ctx, serverA); err != nil || claimed {
		t.Fatalf("blocked server still claimed: %v %v", claimed, err)
	}
	if blocked, err := s.ServerBlocked(ctx, serverA); err != nil || !blocked {
		t.Fatalf("ServerBlocked = %v, %v", blocked, err)
	}
	rows, err := s.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != serverB || rows[0].ClaimedAt.IsZero() ||
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
	if err = s.ClaimServer(ctx, serverA, "alpha", a, testNow); err != nil {
		t.Fatal(err)
	}
	other := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2", Login: "other"}
	if err = s.ClaimServer(ctx, serverB, "beta", other, testNow); err != nil {
		t.Fatal(err)
	}
	kept := edgeproto.DirectoryEntry{Kind: edgeproto.EntryMember, Provider: "github", Subject: "2", Role: "admin"}
	if err = s.ReplaceDirectory(ctx, serverB, []edgeproto.DirectoryEntry{
		kept,
		{Kind: edgeproto.EntryMember, Provider: "github", Subject: "1", Role: "collaborator"},
		{Kind: edgeproto.EntryInvitation, Provider: "github", Login: "owner", Role: "viewer", ExpiresAt: testNow.Add(time.Hour)},
		{Kind: edgeproto.EntryInvitation, Email: "Owner@Example.test", Role: "viewer", ExpiresAt: testNow.Add(time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.BlockAccount(ctx, a.Provider, a.Subject, testNow); err != nil {
		t.Fatal(err)
	}

	got, err := s.DeleteAccount(ctx, a.Provider, a.Subject)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account.Login != "Owner" || !slices.Equal(got.Servers, []string{serverA}) || got.Entries != 3 {
		t.Fatalf("deleted %+v", got)
	}
	if _, err = s.Server(ctx, serverA); !errors.Is(err, ErrNotFound) {
		t.Errorf("owned server kept: %v", err)
	}
	acc, err := s.ServerAccess(ctx, serverB, other)
	if err != nil || len(acc.Entries) != 1 || acc.Entries[0] != kept {
		t.Errorf("other server's directory %+v, %v; want only %+v", acc.Entries, err, kept)
	}
	var left int
	for _, table := range []string{"web_sessions", "devices", "device_authorizations", "web_codes"} {
		var n int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		left += n
	}
	if left != 0 {
		t.Errorf("%d session, device or code rows left", left)
	}
	rows, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The block outlives the account, so the person cannot sign back in.
	if len(rows) != 2 || rows[1].Account.Subject != "1" || !rows[1].Blocked || rows[1].Account.Login != "" {
		t.Errorf("accounts after delete %+v", rows)
	}
	if _, err := s.DeleteAccount(ctx, a.Provider, a.Subject); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

// TestExpiryPrunesUseAnIndex checks the deletes that run before every new
// session, device authorization and web code: unauthenticated requests
// start the latter two, so the delete must not scan its table.
func TestExpiryPrunesUseAnIndex(t *testing.T) {
	s := openStore(t)
	for _, table := range []string{"web_sessions", "device_authorizations", "web_codes"} {
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
