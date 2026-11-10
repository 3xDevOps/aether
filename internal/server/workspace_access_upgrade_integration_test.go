//go:build integration

package server

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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

// preAccessMember is one member row of a database from before
// workspace-scoped access.
type preAccessMember struct {
	id, role string
	pending  bool
	merged   string
	key      ssh.Signer
}

// seedPreAccess writes a v109 database into dataDir holding members, in
// creation order, and each workspace with a completed session, a
// template and a team memory note.
func seedPreAccess(t *testing.T, dataDir string, members []*preAccessMember, workspaces []string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(StorePath(dataDir)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	run := func(q string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(q, args...); err != nil {
			t.Fatalf("seed v%d: %v\n%s", preAccessSchemaVersion, err, q)
		}
	}
	run(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`)
	for i, m := range migrationsThrough(t, preAccessSchemaVersion) {
		run(m)
		run(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, i+1)
	}
	// Rows go in newest first so row order alone cannot pick the super
	// admin.
	for i := len(members) - 1; i >= 0; i-- {
		m := members[i]
		key := ""
		if m.key != nil {
			key = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(m.key.PublicKey())))
		}
		run(`INSERT INTO members (id, display_name, public_key, pending, color, role, merged_into, created_at) VALUES (?, ?, ?, ?, '#3cb44b', ?, ?, ?)`,
			m.id, m.id, key, m.pending, m.role, m.merged, i+1)
	}
	author := ""
	for _, m := range members {
		if !m.pending && m.merged == "" {
			author = m.id
			break
		}
	}
	for i, ws := range workspaces {
		run(`INSERT INTO workspaces (id, name, created_at) VALUES (?, ?, ?)`, ws, strings.TrimPrefix(ws, "ws-"), i+1)
		run(`INSERT INTO runs (id, workspace_id, member_id, account_member_id, home_member_id, requested_account_member_id,
			task, harness, mode, status, branch, worktree, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'old work', 'fake', 'tui', 'completed', 'aether/old', '/old', ?)`, "run-"+ws, ws, author, author, author, author, i+1)
		run(`INSERT INTO templates (id, workspace_id, name, task, harness, mode, created_at) VALUES (?, ?, ?, 'nightly', 'fake', 'headless', ?)`,
			"tpl-"+ws, ws, "nightly-"+ws, i+1)
		run(`INSERT INTO memory (id, workspace_id, kind, source, title, body, author_member_id, hash, created_at, updated_at)
			VALUES (?, ?, 'decision', 'member', ?, 'kept across the upgrade', ?, ?, ?, ?)`, "mem-"+ws, ws, "note-"+ws, author, "hash-"+ws, i+1, i+1)
	}
}

// startUpgraded runs a server on dataDir, which migrates it.
func startUpgraded(ctx context.Context, t *testing.T, dataDir string) (*Server, string) {
	t.Helper()
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
	return srv, waitSSHAddr(t, srv)
}

// reach is what one member reaches through the read surfaces: workspaces
// with their role, sessions, and the templates and memory notes of each
// workspace.
type reach struct {
	Workspaces map[string]string
	Runs       []string
	Templates  []string
	Notes      []string
}

func reachOf(t *testing.T, c *protocol.Client, workspaces []string) reach {
	t.Helper()
	r := reach{Workspaces: map[string]string{}}
	var list protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
		t.Fatalf("workspace.list: %v", err)
	}
	for _, w := range list.Workspaces {
		r.Workspaces[w.ID] = w.Role
	}
	var runs protocol.RunListResult
	if err := c.Call(protocol.MethodRunList, protocol.RunListParams{}, &runs); err != nil {
		t.Fatalf("run.list: %v", err)
	}
	for _, run := range runs.Runs {
		r.Runs = append(r.Runs, run.ID)
	}
	for _, ws := range workspaces {
		var templates protocol.TemplateListResult
		if err := c.Call(protocol.MethodTemplateList, protocol.TemplateListParams{WorkspaceID: ws}, &templates); err == nil {
			for _, tpl := range templates.Templates {
				r.Templates = append(r.Templates, tpl.Name)
			}
		}
		var notes protocol.MemorySearchResult
		if err := c.Call(protocol.MethodMemorySearch, protocol.MemorySearchParams{WorkspaceID: ws, All: true}, &notes); err == nil {
			for _, n := range notes.Items {
				r.Notes = append(r.Notes, n.Title)
			}
		}
	}
	slices.Sort(r.Runs)
	slices.Sort(r.Templates)
	slices.Sort(r.Notes)
	return r
}

// everything is what a member reaching every one of workspaces with role
// reaches.
func everything(workspaces []string, role string) reach {
	r := reach{Workspaces: map[string]string{}}
	for _, ws := range workspaces {
		r.Workspaces[ws] = role
		r.Runs = append(r.Runs, "run-"+ws)
		r.Templates = append(r.Templates, "nightly-"+ws)
		r.Notes = append(r.Notes, "note-"+ws)
	}
	slices.Sort(r.Runs)
	slices.Sort(r.Templates)
	slices.Sort(r.Notes)
	return r
}

func sameReach(a, b reach) bool {
	return maps.Equal(a.Workspaces, b.Workspaces) && slices.Equal(a.Runs, b.Runs) && slices.Equal(a.Templates, b.Templates) && slices.Equal(a.Notes, b.Notes)
}

// TestIntegrationWorkspaceAccessUpgrade is threat case 14: a server from
// before workspace-scoped access starts on its old database. Before,
// every approved member reached every workspace, and after it each still
// reaches exactly that. The earliest approved admin, not the earlier
// pending one nor a merged one, is super admin and owns every workspace;
// the other admins stay admins. A member pending at the upgrade, and one
// invited after it, reach nothing until granted.
func TestIntegrationWorkspaceAccessUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dataDir := filepath.Join(shortTempDir(t), "data")
	key := func() ssh.Signer { _, k := writeClientKey(t); return k }
	members := []*preAccessMember{
		{id: "m-pat", role: "admin", pending: true, key: key()},
		{id: "m-max", role: "admin", merged: "m-bea"},
		{id: "m-ada", role: "admin", key: key()},
		{id: "m-bea", role: "admin", key: key()},
		{id: "m-cy", role: "collaborator", key: key()},
		{id: "m-cal", role: "collaborator", key: key()},
		{id: "m-dee", role: "viewer", key: key()},
		{id: "m-dot", role: "viewer", key: key()},
		{id: "m-eve", role: "collaborator", pending: true, key: key()},
	}
	workspaces := []string{"ws-one", "ws-two", "ws-three"}
	seedPreAccess(t, dataDir, members, workspaces)
	// Before the upgrade every approved, unmerged member reached every
	// workspace with their server role, and the others reached none.
	before := map[string]reach{}
	for _, m := range members {
		if !m.pending && m.merged == "" {
			before[m.id] = everything(workspaces, m.role)
		}
	}

	srv, addr := startUpgraded(ctx, t, dataDir)
	st := srv.Store()
	clients, ctrl := map[string]*ssh.Client{}, map[string]*protocol.Client{}
	for _, m := range members {
		if m.key != nil {
			clients[m.id] = dialSSH(t, addr, m.key)
			ctrl[m.id] = openControl(t, clients[m.id])
		}
	}

	wantRole := map[string]domain.Role{
		"m-pat": domain.RoleAdmin, "m-max": domain.RoleAdmin, "m-ada": domain.RoleSuperAdmin, "m-bea": domain.RoleAdmin,
		"m-cy": domain.RoleCollaborator, "m-cal": domain.RoleCollaborator, "m-dee": domain.RoleViewer, "m-dot": domain.RoleViewer,
		"m-eve": domain.RoleCollaborator,
	}
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(StorePath(dataDir)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	for _, m := range members {
		var (
			role, merged string
			pending      bool
		)
		if err := raw.QueryRowContext(ctx, `SELECT role, pending, merged_into FROM members WHERE id = ?`, m.id).Scan(&role, &pending, &merged); err != nil {
			t.Fatal(err)
		}
		if role != string(wantRole[m.id]) || pending != m.pending || merged != m.merged {
			t.Errorf("%s after the upgrade = role %q pending %v merged %q, want %q %v %q", m.id, role, pending, merged, wantRole[m.id], m.pending, m.merged)
		}
	}
	for _, ws := range workspaces {
		got, err := st.GetWorkspace(ctx, domain.WorkspaceID(ws))
		if err != nil {
			t.Fatal(err)
		}
		if got.OwnerID != "m-ada" {
			t.Errorf("%s is owned by %q, want the super admin m-ada", ws, got.OwnerID)
		}
	}
	// What each member reaches is what they reached before; the super
	// admin acts as admin everywhere and the other admin, granted, as a
	// collaborator.
	for id, want := range before {
		switch id {
		case "m-ada":
			want = everything(workspaces, "admin")
		case "m-bea":
			want = everything(workspaces, "collaborator")
		}
		if got := reachOf(t, ctrl[id], workspaces); !sameReach(got, want) {
			t.Errorf("%s reaches %+v after the upgrade, want %+v", id, got, want)
		}
	}

	ada := ctrl["m-ada"]
	for _, ws := range workspaces {
		for _, c := range []struct {
			method string
			params any
		}{
			{protocol.MethodWorkspaceSettings, protocol.WorkspaceSettingsParams{WorkspaceID: ws}},
			{protocol.MethodBudgetSet, protocol.BudgetSetParams{WorkspaceID: ws, LimitUSD: 25}},
			{protocol.MethodWorkspaceAccessList, protocol.WorkspaceAccessParams{WorkspaceID: ws}},
			{protocol.MethodTemplateSave, protocol.TemplateSaveParams{WorkspaceID: ws, Name: "after-" + ws, Task: "t", Harness: "fake"}},
			{protocol.MethodTemplateDelete, protocol.TemplateDeleteParams{WorkspaceID: ws, Name: "after-" + ws}},
		} {
			if err := ada.Call(c.method, c.params, nil); err != nil {
				t.Errorf("super admin %s in %s: %v", c.method, ws, err)
			}
		}
	}
	if err := ada.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: "m-dot", Role: string(domain.RoleCollaborator)}, nil); err != nil {
		t.Errorf("super admin member.role: %v", err)
	}
	if err := ada.Call(protocol.MethodWorkspaceOwnerSet, protocol.WorkspaceOwnerSetParams{WorkspaceID: "ws-three", MemberID: "m-bea"}, nil); err != nil {
		t.Errorf("super admin workspace.owner.set: %v", err)
	}
	if err := ctrl["m-bea"].Call(protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: "m-dot", Role: string(domain.RoleViewer)}, nil); err == nil {
		t.Error("the admin who is not super admin changed a role")
	}

	if m := memberInfo(t, ctrl["m-eve"]); !m.Pending {
		t.Fatalf("m-eve is %+v after the upgrade, want still pending", m)
	}
	if err := ada.Call(protocol.MethodMemberApprove, protocol.MemberApproveParams{MemberID: "m-eve"}, nil); err != nil {
		t.Fatalf("ada approves m-eve: %v", err)
	}
	if got := reachOf(t, ctrl["m-eve"], workspaces); !sameReach(got, reach{Workspaces: map[string]string{}}) {
		t.Errorf("m-eve, pending at the upgrade, reaches %+v after approval, want nothing", got)
	}
	for _, ws := range workspaces {
		list, err := st.ListWorkspaceGrants(ctx, domain.WorkspaceID(ws))
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, g := range list {
			ids = append(ids, string(g.MemberID))
		}
		slices.Sort(ids)
		want := []string{"m-bea", "m-cal", "m-cy", "m-dee", "m-dot"}
		if ws == "ws-three" {
			// A super admin handing a workspace on keeps no grant.
			want = []string{"m-cal", "m-cy", "m-dee", "m-dot"}
		}
		if !slices.Equal(ids, want) {
			t.Errorf("%s grants = %v, want %v", ws, ids, want)
		}
	}

	assertNewcomer(ctx, t, srv, addr, ada, workspaces)
}

// assertNewcomer invites a member after the upgrade with no workspace:
// they see no workspace, session, template, note, event or workspace
// data of members until granted one, and then exactly that one.
func assertNewcomer(ctx context.Context, t *testing.T, srv *Server, addr string, admin *protocol.Client, workspaces []string) {
	t.Helper()
	var invite protocol.MemberInviteResult
	if err := admin.Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{}, &invite); err != nil {
		t.Fatalf("member.invite with no workspace: %v", err)
	}
	_, key := writeClientKey(t)
	join, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "invite:" + invite.Code + ":nia", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("invite join: %v", err)
	}
	_ = memberInfo(t, openControl(t, join))
	_ = join.Close()
	client := dialSSH(t, addr, key)
	nia := openControl(t, client)
	id := memberInfo(t, nia).ID

	if got := reachOf(t, nia, workspaces); !sameReach(got, reach{Workspaces: map[string]string{}}) {
		t.Errorf("a member invited after the upgrade reaches %+v, want nothing", got)
	}
	for _, ws := range workspaces {
		missing := missingID(ws)
		for _, method := range []string{protocol.MethodWorkspaceGet, protocol.MethodTemplateList, protocol.MethodMemorySearch} {
			params := protocol.WorkspaceGetParams{WorkspaceID: ws}
			assertLikeMissing(t, "newcomer "+method, call(nia, method, params), call(nia, method, protocol.WorkspaceGetParams{WorkspaceID: missing}), ws, missing)
		}
	}
	var attention protocol.AttentionListResult
	if err := nia.Call(protocol.MethodAttentionList, protocol.AttentionListParams{}, &attention); err != nil || len(attention.Items) != 0 {
		t.Errorf("newcomer attention.list = %+v, %v; want nothing", attention.Items, err)
	}
	members := call(nia, protocol.MethodMemberList, struct{}{})
	for _, ws := range workspaces {
		if members.refused() || strings.Contains(members.Result, ws) {
			t.Errorf("newcomer member.list = %+v, want members without %s", members, ws)
		}
	}

	feed := subscribe(t, client, protocol.SubscribeRequest{})
	publish := func(ws, marker string) {
		t.Helper()
		if _, err := srv.Bus().Publish(ctx, events.Event{WorkspaceID: domain.WorkspaceID(ws), RunID: domain.RunID("run-" + ws),
			Payload: events.TimelinePayload{Kind: events.TimelineNote, Message: marker}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ws := range workspaces {
		publish(ws, "before-grant-"+ws)
	}

	if err := admin.Call(protocol.MethodWorkspaceAccessGrant, protocol.WorkspaceAccessGrantParams{WorkspaceID: "ws-two", MemberIDs: []string{id}}, nil); err != nil {
		t.Fatalf("workspace.access.grant ws-two: %v", err)
	}
	want := reach{Workspaces: map[string]string{"ws-two": "collaborator"}, Runs: []string{"run-ws-two"}, Templates: []string{"nightly-ws-two"}, Notes: []string{"note-ws-two"}}
	if got := reachOf(t, nia, workspaces); !sameReach(got, want) {
		t.Errorf("newcomer granted ws-two reaches %+v, want %+v", got, want)
	}
	for _, ws := range workspaces {
		publish(ws, "after-grant-"+ws)
	}
	if missing := feed.waitMarkers(time.Now().Add(15*time.Second), "after-grant-ws-two"); len(missing) > 0 {
		t.Errorf("newcomer feed never carried ws-two's event after the grant")
	}
	for _, e := range feed.events() {
		if slices.Contains(workspaces, e.WorkspaceID) && e.WorkspaceID != "ws-two" {
			t.Errorf("newcomer feed carried %s of %s", e.Type, e.WorkspaceID)
		}
		if strings.Contains(string(e.Payload), "before-grant-") {
			t.Errorf("newcomer feed carried an event from before the grant: %s", e.Payload)
		}
	}
}

// TestIntegrationWorkspaceAccessUpgradeWithoutAdmin is a server with no
// approved admin at the upgrade: none becomes super admin, every member
// keeps every workspace, and aether-server member role on the data
// directory restores administration.
func TestIntegrationWorkspaceAccessUpgradeWithoutAdmin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dataDir := filepath.Join(shortTempDir(t), "data")
	key := func() ssh.Signer { _, k := writeClientKey(t); return k }
	members := []*preAccessMember{
		{id: "m-pat", role: "admin", pending: true, key: key()},
		{id: "m-cy", role: "collaborator", key: key()},
		{id: "m-dee", role: "viewer", key: key()},
	}
	workspaces := []string{"ws-one", "ws-two"}
	seedPreAccess(t, dataDir, members, workspaces)
	srv, addr := startUpgraded(ctx, t, dataDir)
	st := srv.Store()
	ctrl := map[string]*protocol.Client{}
	for _, m := range members {
		ctrl[m.id] = openControl(t, dialSSH(t, addr, m.key))
	}
	list, err := st.ListMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.Role == domain.RoleSuperAdmin {
			t.Errorf("%s became super admin with no approved admin to promote", m.ID)
		}
	}
	for _, m := range members[1:] {
		if got := reachOf(t, ctrl[m.id], workspaces); !sameReach(got, everything(workspaces, m.role)) {
			t.Errorf("%s reaches %+v after the upgrade, want every workspace", m.id, got)
		}
	}
	if err = ctrl["m-cy"].Call(protocol.MethodWorkspaceSettings, protocol.WorkspaceSettingsParams{WorkspaceID: "ws-one"}, nil); err == nil {
		t.Error("a collaborator administered a workspace before being promoted")
	}

	out, err := hostMemberRole(t, dataDir, "m-cy", "super_admin")
	if err != nil || !strings.Contains(out, "is super_admin, was collaborator") {
		t.Fatalf("aether-server member role m-cy super_admin = %q, %v", out, err)
	}
	cy := ctrl["m-cy"]
	if m := memberInfo(t, cy); m.Role != string(domain.RoleSuperAdmin) {
		t.Fatalf("m-cy is %q after aether-server member role, want super_admin", m.Role)
	}
	for _, ws := range workspaces {
		if err := cy.Call(protocol.MethodWorkspaceOwnerSet, protocol.WorkspaceOwnerSetParams{WorkspaceID: ws, MemberID: "m-cy"}, nil); err != nil {
			t.Errorf("restored super admin workspace.owner.set %s: %v", ws, err)
		}
		if err := cy.Call(protocol.MethodWorkspaceSettings, protocol.WorkspaceSettingsParams{WorkspaceID: ws}, nil); err != nil {
			t.Errorf("restored super admin workspace.settings %s: %v", ws, err)
		}
	}
	if err := cy.Call(protocol.MethodMemberApprove, protocol.MemberApproveParams{MemberID: "m-pat"}, nil); err != nil {
		t.Errorf("restored super admin approves m-pat: %v", err)
	}
}

// TestIntegrationWorkspaceAccessLastSuperAdmin is threat case 15 on every
// path, with a pending super admin present who does not count: the last
// approved super admin is neither demoted nor removed over RPC, nor
// demoted with aether-server member role.
func TestIntegrationWorkspaceAccessLastSuperAdmin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dataDir := filepath.Join(shortTempDir(t), "data")
	_, adaKey := writeClientKey(t)
	members := []*preAccessMember{
		{id: "m-ada", role: "admin", key: adaKey},
		{id: "m-pat", role: "admin", pending: true},
		{id: "m-cy", role: "collaborator"},
	}
	seedPreAccess(t, dataDir, members, []string{"ws-one"})
	srv, addr := startUpgraded(ctx, t, dataDir)
	ada := openControl(t, dialSSH(t, addr, adaKey))
	if err := ada.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: "m-pat", Role: string(domain.RoleSuperAdmin)}, nil); err != nil {
		t.Fatalf("make pending m-pat super admin: %v", err)
	}
	for _, c := range []struct {
		method string
		params any
	}{
		{protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: "m-ada", Role: string(domain.RoleAdmin)}},
		{protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: "m-ada", Role: string(domain.RoleViewer)}},
		{protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: "m-ada"}},
	} {
		if o := call(ada, c.method, c.params); o.Code != protocol.CodeDenied || !strings.Contains(o.Message, "last") {
			t.Errorf("%s %+v = %+v, want the last super admin refusal", c.method, c.params, o)
		}
	}
	out, err := hostMemberRole(t, dataDir, "m-ada", "admin")
	if err == nil || !strings.Contains(out, "refusing to demote the last approved super admin") {
		t.Errorf("aether-server member role m-ada admin = %q, %v; want refused", out, err)
	}
	if got, err := srv.Store().GetMember(ctx, "m-ada"); err != nil || got.Role != domain.RoleSuperAdmin {
		t.Errorf("m-ada = %+v, %v; want still super admin", got, err)
	}
}

// hostMemberRole runs the built aether-server member role on dataDir, as
// an operator on the host does, and returns its combined output.
func hostMemberRole(t *testing.T, dataDir, id, role string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildServerBinary(t), "member", "role", id, role, "--data-dir", dataDir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
