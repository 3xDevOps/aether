package main

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
)

const testServer = "aaaaaaaaaaaaaaaaaaaaaaaaaa"

// run runs one operator command against dir and returns what it printed.
func run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := servers
	if args[0] == "accounts" {
		cmd = accounts
	}
	err := cmd(append([]string{args[1], "--data", dir}, args[2:]...), envOf(nil), &out)
	return out.String(), err
}

func TestOperatorCommands(t *testing.T) {
	dir := t.TempDir()
	s, err := edgestore.Open(filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"}
	if _, err = s.SignIn(ctx, owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordClaim(ctx, testServer, "workstation", edgeproto.PolicyAccount, edgeproto.AccountPrincipal(owner), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, dir, "servers", "list")
	if err != nil || !strings.Contains(out, testServer+"  claimed  workstation  github:42  octo") {
		t.Fatalf("servers list:\n%s%v", out, err)
	}
	if _, err = run(t, dir, "servers", "block", testServer); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, dir, "servers", "list")
	if err != nil || !strings.Contains(out, testServer+"  blocked  -") {
		t.Fatalf("servers list after block:\n%s%v", out, err)
	}
	if _, err = run(t, dir, "servers", "remove", testServer); err == nil || !strings.Contains(err.Error(), "is not claimed") {
		t.Fatalf("remove of a blocked server: %v", err)
	}
	if _, err = run(t, dir, "servers", "unblock", testServer); err != nil {
		t.Fatal(err)
	}
	if _, err = run(t, dir, "servers", "unblock", testServer); err == nil || !strings.Contains(err.Error(), "is not blocked") {
		t.Fatalf("second unblock: %v", err)
	}

	s, err = edgestore.Open(filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordClaim(ctx, testServer, "workstation", edgeproto.PolicyAccount, edgeproto.AccountPrincipal(owner), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = run(t, dir, "accounts", "block", "github:42"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, dir, "accounts", "list")
	if err != nil || !strings.Contains(out, "github:42  blocked  octo") {
		t.Fatalf("accounts list:\n%s%v", out, err)
	}
	out, err = run(t, dir, "accounts", "delete", "github:42")
	if err != nil || !strings.Contains(out, "deleted account github:42 (octo): 0 devices; ownerless now: ["+testServer+"]") {
		t.Fatalf("accounts delete:\n%s%v", out, err)
	}
	out, err = run(t, dir, "servers", "list")
	if err != nil || !strings.Contains(out, testServer+"  ownerless  workstation  -") {
		t.Fatalf("servers list after the owner's deletion:\n%s%v", out, err)
	}
	if _, err := run(t, dir, "accounts", "delete", "github:42"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := run(t, dir, "accounts", "unblock", "github:42"); err != nil {
		t.Fatalf("unblock of a deleted account: %v", err)
	}

	for _, bad := range [][]string{
		{"servers", "block", "not-an-id"},
		{"accounts", "block", "42"},
		{"accounts", "block", "gitlab:42"},
		{"accounts", "block", "google:g-1"},
		{"accounts", "delete", "google"},
	} {
		if _, err := run(t, dir, bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

// TestGoogleAccountFromAnEarlierVersion lists and deletes an account a
// build from the v0.5.2-alpha.3 tag created with Google.
func TestGoogleAccountFromAnEarlierVersion(t *testing.T) {
	dir := t.TempDir()
	s, err := edgestore.Open(filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SignIn(context.Background(), edgeproto.Account{Provider: "google", Subject: "g-1", Email: "person@example.test"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, dir, "accounts", "list")
	if err != nil || !strings.Contains(out, "google:g-1  active  -      person@example.test") {
		t.Fatalf("accounts list:\n%s%v", out, err)
	}
	if _, err = run(t, dir, "accounts", "block", "google:g-1"); err == nil ||
		!strings.Contains(err.Error(), `sign-in provider "google" is not supported: Aether signs in with GitHub only`) {
		t.Fatalf("block: %v", err)
	}
	out, err = run(t, dir, "accounts", "delete", "google:g-1")
	if err != nil || !strings.Contains(out, "deleted account google:g-1 (person@example.test): 0 devices") ||
		!strings.Contains(out, "sent the deletion when each next enrolls: []") {
		t.Fatalf("accounts delete:\n%s%v", out, err)
	}
	if out, err = run(t, dir, "accounts", "list"); err != nil || strings.Contains(out, "google") {
		t.Fatalf("accounts list after delete:\n%s%v", out, err)
	}
}

// TestOperatorCommandsOnlyRemoveAndBlock lists every aether-edge command.
// An operator command may list, remove or block; unblock only lifts the
// operator's own block. None may grant: approve a device, record an
// owner, or admit an account to a server. The edge is not a recovery path
// (docs/plans/2026-09-28-edge-access-policies.md, rule 10). A command
// added here needs that review.
func TestOperatorCommandsOnlyRemoveAndBlock(t *testing.T) {
	for _, tt := range []struct {
		group string
		got   []string
		want  []string
	}{
		{"aether-edge", slices.Collect(maps.Keys(commands)), []string{"accounts", "healthcheck", "serve", "servers", "version"}},
		{"aether-edge servers", slices.Collect(maps.Keys(serverCommands)), []string{"block", "list", "remove", "unblock"}},
		{"aether-edge accounts", slices.Collect(maps.Keys(accountCommands)), []string{"block", "delete", "list", "unblock"}},
	} {
		slices.Sort(tt.got)
		if !slices.Equal(tt.got, tt.want) {
			t.Errorf("%s commands are %v, reviewed %v", tt.group, tt.got, tt.want)
		}
	}
}

func TestOperatorCommandsNeedAnEdgeDatabase(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "servers", "list"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list without a database: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "edge.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a database was created: %v", err)
	}
}
