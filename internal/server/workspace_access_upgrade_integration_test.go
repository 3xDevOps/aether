//go:build integration

package server

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// preAccessSchemaVersion is the last schema without workspace-scoped
// access; v110 adds owners and grants.
const preAccessSchemaVersion = 109

// migrationsThrough reads the first n migrations out of
// internal/store/migrate.go, which appends versions and never edits one
// that shipped.
func migrationsThrough(t *testing.T, n int) []string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "internal", "store", "migrate.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var all []string
	ast.Inspect(f, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "migrations" {
			return true
		}
		for _, elt := range spec.Values[0].(*ast.CompositeLit).Elts {
			s, uerr := strconv.Unquote(elt.(*ast.BasicLit).Value)
			if uerr != nil {
				t.Fatalf("migration %d: %v", len(all)+1, uerr)
			}
			all = append(all, s)
		}
		return false
	})
	if len(all) <= n || !strings.Contains(all[n], "CREATE TABLE workspace_grants") {
		t.Fatalf("%s has %d migrations and v%d is not workspace access; update preAccessSchemaVersion", path, len(all), n+1)
	}
	return all[:n]
}

// TestIntegrationWorkspaceAccessUpgrade is threat case 14: a server from
// before workspace-scoped access starts on its old database, and every
// approved member still reaches every workspace. The earliest admin is
// super admin, the other admin stays admin, and a member pending at the
// upgrade is admitted to no workspace.
func TestIntegrationWorkspaceAccessUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dataDir := filepath.Join(shortTempDir(t), "data")
	path := StorePath(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	names := []string{"ada", "bea", "cy", "dee", "eve"}
	keys := map[string]ssh.Signer{}
	for _, n := range names {
		_, keys[n] = writeClientKey(t)
	}
	keyLine := func(n string) string {
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(keys[n].PublicKey())))
	}
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i, m := range migrationsThrough(t, preAccessSchemaVersion) {
		if _, err := raw.Exec(m); err != nil {
			t.Fatalf("apply v%d: %v", i+1, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	// bea is the newer admin, listed first so row order alone does not
	// pick the super admin.
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, pending, color, role, created_at)
			VALUES ('m-bea', 'Bea', ?, 0, '#3cb44b', 'admin', 2),
			       ('m-ada', 'Ada', ?, 0, '#e6194b', 'admin', 1),
			       ('m-cy',  'Cy',  ?, 0, '#ffe119', 'collaborator', 3),
			       ('m-dee', 'Dee', ?, 0, '#4363d8', 'viewer', 4),
			       ('m-eve', 'Eve', ?, 1, '#f58231', 'collaborator', 5);
		INSERT INTO workspaces (id, name, created_at) VALUES ('ws-one', 'one', 1), ('ws-two', 'two', 2);
	`, keyLine("bea"), keyLine("ada"), keyLine("cy"), keyLine("dee"), keyLine("eve")); err != nil {
		t.Fatalf("seed v%d rows: %v", preAccessSchemaVersion, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	srv, err := New(ctx, Config{DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: newE2ERuntime(), StandardImage: "e2e/fake"})
	if err != nil {
		t.Fatalf("server.New on the v%d database: %v", preAccessSchemaVersion, err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("server run: %v", err)
		}
	})
	addr := waitSSHAddr(t, srv)
	ctrl := map[string]*protocol.Client{}
	for _, n := range names {
		ctrl[n] = openControl(t, dialSSH(t, addr, keys[n]))
	}

	wantRole := map[string]domain.Role{
		"ada": domain.RoleSuperAdmin, "bea": domain.RoleAdmin,
		"cy": domain.RoleCollaborator, "dee": domain.RoleViewer,
	}
	// The role each acts with in an upgraded workspace: the super admin
	// owns them all; everyone else, the other admin too, was granted them.
	wantIn := map[string]string{"ada": "admin", "bea": "collaborator", "cy": "collaborator", "dee": "viewer"}
	listed := func(n string) map[string]string {
		var list protocol.WorkspaceListResult
		if err := ctrl[n].Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
			t.Errorf("%s workspace.list: %v", n, err)
			return nil
		}
		got := map[string]string{}
		for _, w := range list.Workspaces {
			got[w.ID] = w.Role
			if w.OwnerMemberID != "m-ada" {
				t.Errorf("%s sees %s owned by %q, want m-ada", n, w.ID, w.OwnerMemberID)
			}
		}
		return got
	}
	for _, n := range []string{"ada", "bea", "cy", "dee"} {
		if m := memberInfo(t, ctrl[n]); m.Role != string(wantRole[n]) {
			t.Errorf("%s is %q after the upgrade, want %q", n, m.Role, wantRole[n])
		}
		got := listed(n)
		for _, ws := range []string{"ws-one", "ws-two"} {
			if got[ws] != wantIn[n] {
				t.Errorf("%s workspace.list = %v, want %s with role %q", n, got, ws, wantIn[n])
			}
		}
	}

	if m := memberInfo(t, ctrl["eve"]); !m.Pending {
		t.Fatalf("eve is %+v after the upgrade, want still pending", m)
	}
	if err := ctrl["ada"].Call(protocol.MethodMemberApprove, protocol.MemberApproveParams{MemberID: "m-eve"}, nil); err != nil {
		t.Fatalf("ada approves eve: %v", err)
	}
	if got := listed("eve"); len(got) != 0 {
		t.Errorf("eve, pending at the upgrade, lists %v after approval, want none", got)
	}
	grants, err := srv.Store().MemberWorkspaces(ctx, "m-eve")
	if err != nil {
		t.Fatal(err)
	}
	if len(grants.Owned)+len(grants.Granted) != 0 {
		t.Errorf("eve reaches %+v after approval, want nothing", grants)
	}
	for _, ws := range []string{"ws-one", "ws-two"} {
		list, err := srv.Store().ListWorkspaceGrants(ctx, domain.WorkspaceID(ws))
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, g := range list {
			ids = append(ids, string(g.MemberID))
		}
		slices.Sort(ids)
		if want := []string{"m-bea", "m-cy", "m-dee"}; !slices.Equal(ids, want) {
			t.Errorf("%s grants = %v, want %v", ws, ids, want)
		}
	}
}
