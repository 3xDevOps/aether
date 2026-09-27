package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edge/edgestore"
	"github.com/3xDevOps/Aether/internal/edgeproto"
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
	if err = s.ClaimServer(ctx, testServer, "workstation", owner, time.Now()); err != nil {
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

	if _, err = run(t, dir, "accounts", "block", "github:42"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, dir, "accounts", "list")
	if err != nil || !strings.Contains(out, "github:42  blocked  octo") {
		t.Fatalf("accounts list:\n%s%v", out, err)
	}
	out, err = run(t, dir, "accounts", "delete", "github:42")
	if err != nil || !strings.Contains(out, "deleted account github:42 (octo): 0 devices, 0 owned servers") {
		t.Fatalf("accounts delete:\n%s%v", out, err)
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
	} {
		if _, err := run(t, dir, bad...); err == nil {
			t.Errorf("%v accepted", bad)
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
