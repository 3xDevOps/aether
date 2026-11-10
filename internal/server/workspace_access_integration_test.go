//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// wsaEnv is the access matrix: super admin S, admin A owning wsA, admin B
// owning wsB, collaborator U and viewer V granted wsA, and N with no
// grant, with live runs of A and U in wsA and of B in wsB.
type wsaEnv struct {
	e   *coordEnv
	srv *coordServer

	members map[string]*wsaMember
	wsA     string
	wsB     string
	runA    string
	runU    string
	runB    string
	// roles is the role each member acts with in each workspace they
	// reach.
	roles map[string]map[string]string
	runs  map[string][]string
	seed  string
}

var wsaNames = []string{"S", "A", "B", "U", "V", "N"}

func (w *wsaEnv) reaches(name, ws string) bool { _, ok := w.roles[name][ws]; return ok }

func (w *wsaEnv) foreignWorkspaces(name string) []string {
	var out []string
	for _, ws := range []string{w.wsA, w.wsB} {
		if !w.reaches(name, ws) {
			out = append(out, ws)
		}
	}
	return out
}

func (w *wsaEnv) foreignRuns(name string) []string {
	var out []string
	for _, ws := range w.foreignWorkspaces(name) {
		out = append(out, w.runs[ws]...)
	}
	return out
}

func (w *wsaEnv) reachableRuns(name string) []string {
	var out []string
	for ws := range w.roles[name] {
		out = append(out, w.runs[ws]...)
	}
	return out
}

func (w *wsaEnv) repoURL(ws string) string {
	return fmt.Sprintf("ssh://aether@%s/%s.git", w.srv.addr, ws)
}

// setupStep records a setup call that failed over its RPC and lets the
// matrix go on with the same state written to the store directly, so one
// missing admin RPC does not hide every read case behind it.
func setupStep(t *testing.T, what string, err error, fallback func() error) {
	t.Helper()
	if err == nil {
		return
	}
	t.Errorf("setup: %s over RPC: %v (continuing with the store)", what, err)
	if ferr := fallback(); ferr != nil {
		t.Fatalf("setup: %s in the store: %v", what, ferr)
	}
}

// TestIntegrationWorkspaceAccessMatrix drives the design's threat cases
// (docs/plans/2026-11-10-workspace-access.md) against one wired server:
// every member against every surface, every unreachable id compared with a
// missing one.
func TestIntegrationWorkspaceAccessMatrix(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")

	w := newWSAEnv(ctx, t)

	t.Run("reads", func(t *testing.T) {
		for _, name := range wsaNames {
			t.Run(name, func(t *testing.T) { w.assertReads(t, name) })
		}
	})
	t.Run("events", func(t *testing.T) { w.assertEvents(ctx, t) })
	t.Run("attach", func(t *testing.T) {
		for _, name := range wsaNames {
			t.Run(name, func(t *testing.T) { w.assertAttach(t, name) })
		}
	})
	t.Run("git", func(t *testing.T) {
		for _, name := range wsaNames {
			t.Run(name, func(t *testing.T) { w.assertGit(t, name) })
		}
	})
	t.Run("launch", func(t *testing.T) {
		for _, name := range wsaNames {
			t.Run(name, func(t *testing.T) { w.assertLaunch(t, name) })
		}
	})
	for _, check := range []struct {
		name   string
		assert func(*testing.T, string)
	}{
		{"writes", w.assertForeignWrites},
		{"workspace roles", w.assertWorkspaceRoles},
		{"memory", w.assertMemory},
		{"counts", w.assertCounts},
		{"connector", func(t *testing.T, name string) { w.assertTools(t, name, w.connectorSession(t, name)) }},
		{"assistant", func(t *testing.T, name string) { w.assertTools(t, name, w.assistantSession(t, name)) }},
	} {
		t.Run(check.name, func(t *testing.T) {
			for _, name := range wsaNames {
				t.Run(name, func(t *testing.T) { check.assert(t, name) })
			}
		})
	}
	t.Run("account sharing", func(t *testing.T) { w.assertAccountSharing(t) })
	t.Run("coordination", func(t *testing.T) { w.assertCoordination(ctx, t) })
	t.Run("admin", func(t *testing.T) { w.assertAdminActions(t) })
	t.Run("handoff", func(t *testing.T) { w.assertHandoff(t) })
	t.Run("cli", func(t *testing.T) { w.assertCLI(t) })
	t.Run("revoke in use", func(t *testing.T) { w.assertRevokeInUse(ctx, t) })
	t.Run("roles", func(t *testing.T) { w.assertRoles(t) })
	t.Run("self demotion", func(t *testing.T) { w.assertSelfDemotion(t) })
	t.Run("children", func(t *testing.T) { w.assertChildren(ctx, t) })
}

func newWSAEnv(ctx context.Context, t *testing.T) *wsaEnv {
	t.Helper()
	e := &coordEnv{rt: newE2ERuntime(), image: "e2e/fake", dataDir: filepath.Join(shortTempDir(t), "d")}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	for _, task := range []string{"hold-a", "hold-u", "hold-b"} {
		e.e2e(t).script(task, func(c *e2eContainer) {
			select {
			case <-release:
			case <-c.done:
			}
		})
	}
	w := &wsaEnv{e: e, srv: e.start(ctx, t, false), members: map[string]*wsaMember{}}
	st := w.srv.srv.Store()

	connect := func(name string, m *wsaMember) {
		m.client = dialSSH(t, w.srv.addr, m.key)
		m.ctrl = openControl(t, m.client)
		m.id = memberInfo(t, m.ctrl).ID
		w.members[name] = m
	}

	// S is the first key to reach a fresh server.
	sPath, sKey := writeClientKey(t)
	connect("S", &wsaMember{name: "S", key: sKey, keyPath: sPath})
	S := w.members["S"]
	sRole := memberInfo(t, S.ctrl).Role

	// Everyone else joins with an invite code and their own key.
	for _, name := range wsaNames[1:] {
		var invite protocol.MemberInviteResult
		if err := S.ctrl.Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{}, &invite); err != nil {
			t.Fatalf("member.invite for %s: %v", name, err)
		}
		path, key := writeClientKey(t)
		join, err := ssh.Dial("tcp", w.srv.addr, &ssh.ClientConfig{
			User:            "invite:" + invite.Code + ":" + name,
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		})
		if err != nil {
			t.Fatalf("invite join for %s: %v", name, err)
		}
		_ = memberInfo(t, openControl(t, join))
		_ = join.Close()
		connect(name, &wsaMember{name: name, key: key, keyPath: path})
	}
	setRole := func(name string, role domain.Role) {
		m := w.members[name]
		err := S.ctrl.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: m.id, Role: string(role)}, nil)
		setupStep(t, "member.role "+name+" "+string(role), err, func() error {
			row, gerr := st.GetMember(ctx, domain.MemberID(m.id))
			if gerr != nil {
				return gerr
			}
			row.Role = role
			return st.UpdateMember(ctx, row)
		})
	}
	setRole("A", domain.RoleAdmin)
	setRole("B", domain.RoleAdmin)
	setRole("V", domain.RoleViewer)
	if sRole != string(domain.RoleSuperAdmin) {
		t.Errorf("setup: the bootstrap member is %q, want super_admin (continuing with the store)", sRole)
		row, err := st.GetMember(ctx, domain.MemberID(S.id))
		if err != nil {
			t.Fatal(err)
		}
		row.Role = domain.RoleSuperAdmin
		if err := st.UpdateMember(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	addWorkspace := func(owner, name string) string {
		m := w.members[owner]
		var added protocol.WorkspaceAddResult
		if err := m.ctrl.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: name}, &added); err != nil {
			t.Fatalf("%s workspace.add %s: %v", owner, name, err)
		}
		if added.Workspace.OwnerMemberID != m.id {
			setupStep(t, "workspace.add owner", fmt.Errorf("%s is owned by %q, want its creator %s", name, added.Workspace.OwnerMemberID, m.id), func() error {
				_, err := st.SetWorkspaceOwner(ctx, domain.WorkspaceID(added.Workspace.ID), domain.MemberID(m.id), "")
				return err
			})
		}
		return added.Workspace.ID
	}
	w.wsA = addWorkspace("A", "access-a")
	w.wsB = addWorkspace("B", "access-b")

	A, B := w.members["A"], w.members["B"]
	grantIDs := []string{w.members["U"].id, w.members["V"].id}
	err := A.ctrl.Call(protocol.MethodWorkspaceAccessGrant, protocol.WorkspaceAccessGrantParams{WorkspaceID: w.wsA, MemberIDs: grantIDs}, nil)
	setupStep(t, "workspace.access.grant U V into wsA", err, func() error {
		for _, id := range grantIDs {
			if gerr := st.GrantWorkspace(ctx, domain.WorkspaceID(w.wsA), domain.MemberID(id), domain.MemberID(A.id)); gerr != nil {
				return gerr
			}
		}
		return nil
	})
	w.roles = map[string]map[string]string{
		"S": {w.wsA: "admin", w.wsB: "admin"},
		"A": {w.wsA: "admin"},
		"B": {w.wsB: "admin"},
		"U": {w.wsA: "collaborator"},
		"V": {w.wsA: "viewer"},
		"N": {},
	}

	// Each owner seeds their repository over the SSH git transport.
	w.seed = t.TempDir()
	runGit(t, w.seed, nil, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(w.seed, "README.md"), "# access seed\n")
	runGit(t, w.seed, nil, "add", "-A")
	runGit(t, w.seed, nil, "-c", "user.name=E2E", "-c", "user.email=e2e@localhost", "-c", "commit.gpgsign=false",
		"commit", "-q", "-m", "readme")
	for owner, ws := range map[string]string{"A": w.wsA, "B": w.wsB} {
		if code, stderr := gitAs(w.members[owner], w.seed, "push", "-q", w.repoURL(ws), "main"); code != 0 {
			t.Fatalf("%s seeds %s: exit %d: %s", owner, ws, code, stderr)
		}
	}

	launch := func(name, ws, task string) string {
		var launched protocol.RunResult
		if err := w.members[name].ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
			WorkspaceID: ws, Task: task, Harness: "fake", Mode: string(domain.LaunchTUI),
		}, &launched); err != nil {
			t.Fatalf("%s run.launch %s in %s: %v", name, task, ws, err)
		}
		return launched.Run.ID
	}
	w.runA = launch("A", w.wsA, "hold-a")
	w.runU = launch("U", w.wsA, "hold-u")
	w.runB = launch("B", w.wsB, "hold-b")
	w.runs = map[string][]string{w.wsA: {w.runA, w.runU}, w.wsB: {w.runB}}

	// Something of each workspace in every list a leak would show in.
	for owner, ws := range map[string]string{"A": w.wsA, "B": w.wsB} {
		m := w.members[owner]
		if err := m.ctrl.Call(protocol.MethodMemoryAdd, protocol.MemoryAddParams{
			WorkspaceID: ws, Kind: protocol.MemoryKindConvention, Text: "access-note-" + ws,
		}, nil); err != nil {
			t.Errorf("setup: %s memory.add: %v", owner, err)
		}
		if err := m.ctrl.Call(protocol.MethodTemplateSave, protocol.TemplateSaveParams{
			WorkspaceID: ws, Name: "access-" + owner, Task: "nightly", Harness: "fake",
		}, nil); err != nil {
			t.Errorf("setup: %s template.save: %v", owner, err)
		}
		if err := m.ctrl.Call(protocol.MethodPresenceHeartbeat, protocol.PresenceHeartbeatParams{WorkspaceID: ws}, nil); err != nil {
			t.Errorf("setup: %s presence.heartbeat: %v", owner, err)
		}
	}
	waitRoster(t, S.ctrl, "", func(entries []protocol.PresenceEntry) bool {
		var a, b bool
		for _, e := range entries {
			a = a || e.MemberID == A.id
			b = b || e.MemberID == B.id
		}
		return a && b
	})
	return w
}

func (w *wsaEnv) insightsParams(ws string) protocol.InsightsParams {
	now := time.Now().UTC()
	return protocol.InsightsParams{
		WorkspaceID: ws, Scope: protocol.InsightsScopeEveryone,
		From: now.Add(-24 * time.Hour).Format(time.RFC3339), To: now.Add(24 * time.Hour).Format(time.RFC3339),
	}
}

// wsProbes are the by-workspace reads, each as the params naming ws.
func (w *wsaEnv) wsProbes() []struct {
	method string
	params func(ws string) any
} {
	return []struct {
		method string
		params func(ws string) any
	}{
		{protocol.MethodWorkspaceGet, func(ws string) any { return protocol.WorkspaceGetParams{WorkspaceID: ws} }},
		{protocol.MethodRunList, func(ws string) any { return protocol.RunListParams{WorkspaceID: ws} }},
		{protocol.MethodAttentionList, func(ws string) any { return protocol.AttentionListParams{WorkspaceID: ws} }},
		{protocol.MethodInsightsGet, func(ws string) any { return w.insightsParams(ws) }},
		{protocol.MethodChangesList, func(ws string) any { return protocol.ChangesListParams{WorkspaceID: ws} }},
		{protocol.MethodPresenceRoster, func(ws string) any { return protocol.PresenceRosterParams{WorkspaceID: ws} }},
		{protocol.MethodWorkspaceTimeline, func(ws string) any { return protocol.WorkspaceTimelineParams{WorkspaceID: ws} }},
		{protocol.MethodCostReport, func(ws string) any { return protocol.CostReportParams{WorkspaceID: ws} }},
		{protocol.MethodBudgetGet, func(ws string) any { return protocol.BudgetGetParams{WorkspaceID: ws} }},
		{protocol.MethodTemplateList, func(ws string) any { return protocol.TemplateListParams{WorkspaceID: ws} }},
		{protocol.MethodScheduleList, func(ws string) any { return protocol.ScheduleListParams{WorkspaceID: ws} }},
		{protocol.MethodMemorySearch, func(ws string) any { return protocol.MemorySearchParams{WorkspaceID: ws} }},
		{protocol.MethodFilesRead, func(ws string) any { return protocol.FilesReadParams{WorkspaceID: ws, Path: "README.md"} }},
		{protocol.MethodWorkspaceAccessList, func(ws string) any { return protocol.WorkspaceAccessParams{WorkspaceID: ws} }},
	}
}

// runProbes are the by-run reads.
var runProbes = []struct {
	method string
	params func(run string) any
}{
	{protocol.MethodRunGet, func(run string) any { return protocol.RunIDParams{RunID: run} }},
	{protocol.MethodRunPull, func(run string) any { return protocol.RunIDParams{RunID: run} }},
	{protocol.MethodRunACPHistory, func(run string) any { return protocol.RunACPHistoryParams{RunID: run} }},
	{protocol.MethodChangesList, func(run string) any { return protocol.ChangesListParams{RunID: run} }},
	{protocol.MethodPresenceRoster, func(run string) any { return protocol.PresenceRosterParams{RunID: run} }},
}

func decode[T any](t *testing.T, what string, o outcome) (T, bool) {
	t.Helper()
	var v T
	if o.refused() {
		t.Errorf("%s refused: %+v", what, o)
		return v, false
	}
	if err := json.Unmarshal([]byte(o.Result), &v); err != nil {
		t.Errorf("%s: decode %q: %v", what, o.Result, err)
		return v, false
	}
	return v, true
}

// assertReads is threat cases 1, 2 and 6 for one member: lists and
// aggregates carry only reachable workspaces, by-id reads of an
// unreachable id read as a missing one, reachable ones answer.
func (w *wsaEnv) assertReads(t *testing.T, name string) {
	m := w.members[name]
	foreign := w.foreignWorkspaces(name)
	isForeignWS := func(ws string) bool { return slices.Contains(foreign, ws) }
	isForeignRun := func(run string) bool { return slices.Contains(w.foreignRuns(name), run) }

	t.Run("workspace.list", func(t *testing.T) {
		list, ok := decode[protocol.WorkspaceListResult](t, "workspace.list", call(m.ctrl, protocol.MethodWorkspaceList, struct{}{}))
		if !ok {
			return
		}
		got := map[string]string{}
		for _, ws := range list.Workspaces {
			got[ws.ID] = ws.Role
		}
		want := w.roles[name]
		if len(got) != len(want) {
			t.Errorf("workspace.list = %v, want %v", got, want)
		}
		for ws, role := range want {
			if got[ws] != role {
				t.Errorf("workspace.list role in %s = %q, want %q", ws, got[ws], role)
			}
		}
		for _, ws := range list.Workspaces {
			owner := map[string]string{w.wsA: w.members["A"].id, w.wsB: w.members["B"].id}[ws.ID]
			if ws.OwnerMemberID != owner {
				t.Errorf("workspace.list owner of %s = %q, want %q", ws.ID, ws.OwnerMemberID, owner)
			}
		}
	})
	t.Run("by workspace id", func(t *testing.T) {
		for _, p := range w.wsProbes() {
			for _, ws := range foreign {
				missing := missingID(ws)
				assertLikeMissing(t, p.method, call(m.ctrl, p.method, p.params(ws)), call(m.ctrl, p.method, p.params(missing)), ws, missing)
			}
		}
		for ws := range w.roles[name] {
			if o := call(m.ctrl, protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: ws}); o.refused() {
				t.Errorf("workspace.get of reachable %s refused: %+v", ws, o)
			}
			if o := call(m.ctrl, protocol.MethodFilesRead, protocol.FilesReadParams{WorkspaceID: ws, Path: "README.md"}); o.refused() {
				t.Errorf("files.read in reachable %s refused: %+v", ws, o)
			}
		}
	})
	t.Run("by run id", func(t *testing.T) {
		for _, p := range runProbes {
			for _, run := range w.foreignRuns(name) {
				missing := missingID(run)
				assertLikeMissing(t, p.method, call(m.ctrl, p.method, p.params(run)), call(m.ctrl, p.method, p.params(missing)), run, missing)
			}
		}
		for _, run := range w.reachableRuns(name) {
			if o := call(m.ctrl, protocol.MethodRunGet, protocol.RunIDParams{RunID: run}); o.refused() {
				t.Errorf("run.get of reachable %s refused: %+v", run, o)
			}
		}
	})
	t.Run("run.list", func(t *testing.T) {
		list, ok := decode[protocol.RunListResult](t, "run.list", call(m.ctrl, protocol.MethodRunList, protocol.RunListParams{}))
		if !ok {
			return
		}
		var got []string
		for _, r := range list.Runs {
			got = append(got, r.ID)
			if isForeignRun(r.ID) || isForeignWS(r.WorkspaceID) {
				t.Errorf("run.list carries %s of unreachable %s", r.ID, r.WorkspaceID)
			}
		}
		for _, run := range w.reachableRuns(name) {
			if !slices.Contains(got, run) {
				t.Errorf("run.list = %v, missing reachable run %s", got, run)
			}
		}
	})
	t.Run("attention.list", func(t *testing.T) {
		list, ok := decode[protocol.AttentionListResult](t, "attention.list", call(m.ctrl, protocol.MethodAttentionList, protocol.AttentionListParams{}))
		for _, it := range list.Items {
			if ok && (isForeignWS(it.WorkspaceID) || isForeignRun(it.RunID)) {
				t.Errorf("attention.list carries %+v", it)
			}
		}
	})
	t.Run("insights.get", func(t *testing.T) {
		res, ok := decode[protocol.InsightsResult](t, "insights.get", call(m.ctrl, protocol.MethodInsightsGet, w.insightsParams("")))
		if !ok {
			return
		}
		for _, ws := range res.ByWorkspace {
			if isForeignWS(ws.WorkspaceID) {
				t.Errorf("insights.get by_workspace carries unreachable %+v", ws)
			}
		}
		if want := len(w.reachableRuns(name)); res.Totals.Launched > want {
			t.Errorf("insights.get launched = %d, want at most the %d runs of reachable workspaces", res.Totals.Launched, want)
		}
		if name == "N" && (res.Totals != protocol.InsightsTotals{}) {
			t.Errorf("insights.get totals for a member with no workspace = %+v, want zero", res.Totals)
		}
		for ws := range w.roles[name] {
			if o := call(m.ctrl, protocol.MethodInsightsGet, w.insightsParams(ws)); o.refused() {
				t.Errorf("insights.get of reachable %s refused: %+v", ws, o)
			}
		}
	})
	t.Run("changes.list", func(t *testing.T) {
		res, ok := decode[protocol.ChangesListResult](t, "changes.list", call(m.ctrl, protocol.MethodChangesList, protocol.ChangesListParams{}))
		for _, c := range res.Changes {
			if ok && (isForeignWS(c.WorkspaceID) || isForeignRun(c.RunID)) {
				t.Errorf("changes.list carries %s %s of %s", c.Kind, c.RunID, c.WorkspaceID)
			}
		}
	})
	t.Run("run.overlaps", func(t *testing.T) {
		res, ok := decode[protocol.RunOverlapsResult](t, "run.overlaps", call(m.ctrl, protocol.MethodRunOverlaps, struct{}{}))
		for _, o := range res.Overlaps {
			if ok && isForeignRun(o.RunID) {
				t.Errorf("run.overlaps carries unreachable %s", o.RunID)
			}
			for _, p := range o.With {
				if ok && isForeignRun(p.RunID) {
					t.Errorf("run.overlaps carries unreachable peer %s", p.RunID)
				}
			}
		}
	})
	t.Run("presence.roster", func(t *testing.T) {
		res, ok := decode[protocol.PresenceRosterResult](t, "presence.roster", call(m.ctrl, protocol.MethodPresenceRoster, protocol.PresenceRosterParams{}))
		if !ok {
			return
		}
		for _, e := range res.Members {
			for owner, ws := range map[string]string{"A": w.wsA, "B": w.wsB} {
				if name != owner && !w.reaches(name, ws) && e.MemberID == w.members[owner].id {
					t.Errorf("presence.roster shows %s, present only in unreachable %s", owner, ws)
				}
			}
			for _, run := range e.Watching {
				if isForeignRun(run) {
					t.Errorf("presence.roster shows %s watching unreachable %s", e.MemberID, run)
				}
			}
		}
	})
	t.Run("audit.list", func(t *testing.T) {
		o := call(m.ctrl, protocol.MethodAuditList, protocol.AuditListParams{Limit: 200})
		if name != "S" && o.refused() {
			return
		}
		res, ok := decode[protocol.AuditListResult](t, "audit.list", o)
		for _, e := range res.Entries {
			if ok && name != "S" && e.MemberID != m.id && w.roles[name][e.WorkspaceID] != "admin" {
				t.Errorf("audit.list carries %s by %s in %q, a workspace %s does not administer", e.Method, e.MemberID, e.WorkspaceID, name)
			}
		}
	})
}

// assertEvents is threat case 3: no event of an unreachable workspace
// reaches a member's feed, filtered or not, while both workspaces emit.
func (w *wsaEnv) assertEvents(ctx context.Context, t *testing.T) {
	type sub struct {
		name   string
		stream *eventStream
	}
	var subs []sub
	for _, name := range wsaNames {
		m := w.members[name]
		subs = append(subs, sub{name, subscribe(t, m.client, protocol.SubscribeRequest{})})
		for _, ws := range w.foreignWorkspaces(name) {
			missing := missingID(ws)
			foreign := subscribe(t, m.client, protocol.SubscribeRequest{WorkspaceID: ws})
			assertLikeMissing(t, name+" subscribe "+ws, foreign.ackOutcome(), subscribe(t, m.client, protocol.SubscribeRequest{WorkspaceID: missing}).ackOutcome(), ws, missing)
			if foreign.ack.OK {
				subs = append(subs, sub{name + " filtered", foreign})
			}
		}
	}
	marker := map[string]string{w.wsA: "access-event-" + w.wsA, w.wsB: "access-event-" + w.wsB}
	for ws, run := range map[string]string{w.wsA: w.runA, w.wsB: w.runB} {
		if _, err := w.srv.srv.Bus().Publish(ctx, events.Event{
			WorkspaceID: domain.WorkspaceID(ws), RunID: domain.RunID(run),
			Payload: events.TimelinePayload{Kind: events.TimelineNote, Message: marker[ws]},
		}); err != nil {
			t.Fatalf("publish in %s: %v", ws, err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for _, s := range subs {
		name, _, _ := strings.Cut(s.name, " ")
		var want []string
		if s.name == name {
			for ws := range w.roles[name] {
				want = append(want, marker[ws])
			}
		}
		if len(want) == 0 {
			want = []string{"never-sent"}
		}
		if missing := s.stream.waitMarkers(deadline, want...); len(missing) > 0 && want[0] != "never-sent" {
			t.Errorf("%s feed never carried %v from reachable workspaces", s.name, missing)
		}
		for _, e := range s.stream.events() {
			if slices.Contains(w.foreignWorkspaces(name), e.WorkspaceID) || slices.Contains(w.foreignRuns(name), e.RunID) {
				t.Errorf("%s feed carried %s of unreachable %s (run %s)", s.name, e.Type, e.WorkspaceID, e.RunID)
			}
		}
	}
}

// assertAttach is threat case 5: a read-only attach to a run of an
// unreachable workspace is refused as for a missing run.
func (w *wsaEnv) assertAttach(t *testing.T, name string) {
	m := w.members[name]
	for _, run := range w.foreignRuns(name) {
		missing := missingID(run)
		f, mi := observe(t, m.client, run), observe(t, m.client, missing)
		assertLikeMissing(t, "read-only attach", outcome{Code: f.Code, Message: f.Error, Result: fmt.Sprint(f.OK)},
			outcome{Code: mi.Code, Message: mi.Error, Result: fmt.Sprint(mi.OK)}, run, missing)
	}
	for _, run := range w.reachableRuns(name) {
		if ack := observe(t, m.client, run); !ack.OK {
			t.Errorf("read-only attach to reachable %s refused: %+v", run, ack)
		}
	}
}

// assertGit is threat case 4: clone, fetch and push of an unreachable
// workspace fail as for a missing repository; a viewer fetches and cannot
// push.
func (w *wsaEnv) assertGit(t *testing.T, name string) {
	m := w.members[name]
	for _, ws := range w.foreignWorkspaces(name) {
		missing := missingID(ws)
		for _, op := range [][]string{
			{"ls-remote"},
			{"fetch", "-q"},
			{"push", "-q"},
		} {
			args := func(url string) []string {
				a := append(append([]string{}, op...), url)
				if op[0] == "fetch" {
					a = append(a, "main")
				}
				if op[0] == "push" {
					a = append(a, "main:refs/heads/access-"+strings.ToLower(name))
				}
				return a
			}
			fc, fe := gitAs(m, w.seed, args(w.repoURL(ws))...)
			mc, me := gitAs(m, w.seed, args(w.repoURL(missing))...)
			if fc == 0 {
				t.Errorf("git %s of unreachable %s succeeded", op[0], ws)
			}
			assertLikeMissing(t, "git "+op[0], outcome{Code: fc, Message: fe}, outcome{Code: mc, Message: me}, ws, missing)
		}
	}
	for ws, role := range w.roles[name] {
		if code, stderr := gitAs(m, w.seed, "fetch", "-q", w.repoURL(ws), "main"); code != 0 {
			t.Errorf("git fetch of reachable %s: exit %d: %s", ws, code, stderr)
		}
		code, stderr := gitAs(m, w.seed, "push", "-q", w.repoURL(ws), "main:refs/heads/access-"+strings.ToLower(name))
		if role == "viewer" && code == 0 {
			t.Errorf("a viewer pushed to %s", ws)
		}
		if role != "viewer" && code != 0 {
			t.Errorf("%s push to %s: exit %d: %s", role, ws, code, stderr)
		}
	}
}

// assertLaunch is threat case 11's launch half: a launch into an
// unreachable workspace, or with one as an extra workspace, is refused as
// for a missing one.
func (w *wsaEnv) assertLaunch(t *testing.T, name string) {
	m := w.members[name]
	params := func(ws string, extra ...string) protocol.RunLaunchParams {
		return protocol.RunLaunchParams{WorkspaceID: ws, ExtraWorkspaceIDs: extra, Task: "access-probe", Harness: "fake", Mode: string(domain.LaunchTUI)}
	}
	for _, ws := range w.foreignWorkspaces(name) {
		missing := missingID(ws)
		f := call(m.ctrl, protocol.MethodRunLaunch, params(ws))
		if !f.refused() {
			t.Errorf("run.launch into unreachable %s started a run", ws)
		}
		assertLikeMissing(t, "run.launch", f, call(m.ctrl, protocol.MethodRunLaunch, params(missing)), ws, missing)
		for own, role := range w.roles[name] {
			if role == "viewer" {
				continue
			}
			f := call(m.ctrl, protocol.MethodRunLaunch, params(own, ws))
			if !f.refused() {
				t.Errorf("run.launch in %s with unreachable extra %s started a run", own, ws)
			}
			assertLikeMissing(t, "run.launch extra workspace", f, call(m.ctrl, protocol.MethodRunLaunch, params(own, missing)), ws, missing)
		}
	}
}

// assertCoordination is threat case 12: U's run cannot message a run of
// wsB, and the refusal reads as one for a run that does not exist.
func (w *wsaEnv) assertCoordination(ctx context.Context, t *testing.T) {
	socket := waitCoordSocket(t, w.e.coordDir(w.runU))
	send := func(to, key string) outcome {
		return coordCall(ctx, socket, protocol.MethodCoordSend, protocol.CoordSendParams{ToRunID: to, Body: "access probe", IdempotencyKey: key})
	}
	missing := missingID(w.runB)
	foreign := send(w.runB, "access-foreign")
	if !foreign.refused() {
		t.Errorf("U's run messaged B's run in wsB: %+v", foreign)
	}
	assertLikeMissing(t, "coord.send", foreign, send(missing, "access-missing"), w.runB, missing)
	if o := send(w.runA, "access-peer"); o.refused() {
		t.Errorf("U's run messaging A's run in the same workspace refused: %+v", o)
	}
}

// assertAdminActions is threat cases 7, 8 and 9: an admin administers
// only the workspaces they own, and nothing server-wide.
func (w *wsaEnv) assertAdminActions(t *testing.T) {
	A, B, U, V, S := w.members["A"], w.members["B"], w.members["U"], w.members["V"], w.members["S"]
	missing := missingID(w.wsB)
	denied := func(what string, o outcome) {
		t.Helper()
		if o.Code != protocol.CodeDenied {
			t.Errorf("%s = %+v, want denied", what, o)
		}
	}

	grant := func(ws string, ids ...string) outcome {
		return call(A.ctrl, protocol.MethodWorkspaceAccessGrant, protocol.WorkspaceAccessGrantParams{WorkspaceID: ws, MemberIDs: ids})
	}
	f := grant(w.wsB, U.id)
	if f.Code != protocol.CodeNotFound {
		t.Errorf("A grants U into wsB = %+v, want not found", f)
	}
	assertLikeMissing(t, "A grants U into wsB", f, grant(missing, U.id), w.wsB, missing)
	denied("A grants admin B into wsA", grant(w.wsA, B.id))
	denied("U grants V into wsA", call(U.ctrl, protocol.MethodWorkspaceAccessGrant, protocol.WorkspaceAccessGrantParams{WorkspaceID: w.wsA, MemberIDs: []string{V.id}}))

	owner := func(ws, to string) outcome {
		return call(A.ctrl, protocol.MethodWorkspaceOwnerSet, protocol.WorkspaceOwnerSetParams{WorkspaceID: ws, MemberID: to})
	}
	denied("A sets wsA's owner to B", owner(w.wsA, B.id))
	assertLikeMissing(t, "A sets wsB's owner", owner(w.wsB, A.id), owner(missing, A.id), w.wsB, missing)

	invite := func(ws string) outcome {
		return call(A.ctrl, protocol.MethodMemberInvite, protocol.MemberInviteParams{WorkspaceIDs: []string{ws}})
	}
	f = invite(w.wsB)
	if !f.refused() {
		t.Errorf("A invited into wsB: %+v", f)
	}
	assertLikeMissing(t, "A invites into wsB", f, invite(missing), w.wsB, missing)
	if o := call(A.ctrl, protocol.MethodMemberInvite, protocol.MemberInviteParams{}); !o.refused() {
		t.Errorf("A invited into no workspace: %+v", o)
	}
	if o := invite(w.wsA); o.refused() {
		t.Errorf("A invites into owned wsA refused: %+v", o)
	}

	denied("A promotes U to admin", call(A.ctrl, protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: U.id, Role: string(domain.RoleAdmin)}))
	denied("A demotes S", call(A.ctrl, protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: S.id, Role: string(domain.RoleCollaborator)}))
	denied("A removes U", call(A.ctrl, protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: U.id}))

	audit, ok := decode[protocol.AuditListResult](t, "S audit.list", call(S.ctrl, protocol.MethodAuditList, protocol.AuditListParams{Limit: 200}))
	sawA := false
	for _, e := range audit.Entries {
		sawA = sawA || (e.MemberID == A.id && e.Method == protocol.MethodMemberRole)
	}
	if ok && !sawA {
		t.Errorf("a super admin's audit.list has no entry of A's member.role calls: %+v", audit.Entries)
	}
}

// assertHandoff is threat case 11's handoff half: a run cannot be handed
// to a member who does not reach its workspace.
func (w *wsaEnv) assertHandoff(t *testing.T) {
	U, N := w.members["U"], w.members["N"]
	if o := call(U.ctrl, protocol.MethodRunHandoff, protocol.RunHandoffParams{RunID: w.runU, ToMemberID: N.id}); !o.refused() {
		t.Errorf("U handed run %s in wsA to N, who does not reach it", w.runU)
	}
	var got protocol.RunResult
	if err := U.ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: w.runU}, &got); err != nil {
		t.Errorf("run.get after refused handoff: %v", err)
	} else if got.Run.MemberID != U.id {
		t.Errorf("run %s belongs to %s after a refused handoff, want U", w.runU, got.Run.MemberID)
	}
}

// assertRevokeInUse is threat case 13: A cannot revoke U from wsA while U
// has a live run there.
func (w *wsaEnv) assertRevokeInUse(ctx context.Context, t *testing.T) {
	A, U := w.members["A"], w.members["U"]
	o := call(A.ctrl, protocol.MethodWorkspaceAccessRevoke, protocol.WorkspaceAccessRevokeParams{WorkspaceID: w.wsA, MemberID: U.id})
	if o.Code != protocol.CodeConflict {
		t.Errorf("A revokes U from wsA with U's run live = %+v, want in use (%d)", o, protocol.CodeConflict)
	}
	if !o.refused() {
		if err := w.srv.srv.Store().GrantWorkspace(ctx, domain.WorkspaceID(w.wsA), domain.MemberID(U.id), domain.MemberID(A.id)); err != nil {
			t.Fatalf("restore U's grant: %v", err)
		}
	}
}

// assertRoles is threat cases 8 and 15: only a super admin changes roles,
// and the last super admin can be neither demoted nor removed.
func (w *wsaEnv) assertRoles(t *testing.T) {
	S, A, U := w.members["S"], w.members["A"], w.members["U"]
	role := func(by *wsaMember, m *wsaMember, r domain.Role) outcome {
		return call(by.ctrl, protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: m.id, Role: string(r)})
	}
	for _, r := range []domain.Role{domain.RoleAdmin, domain.RoleCollaborator} {
		if o := role(S, U, r); o.refused() {
			t.Errorf("S sets U to %s: %+v", r, o)
		}
	}
	if o := role(S, S, domain.RoleAdmin); !o.refused() {
		t.Errorf("the last super admin demoted themselves: %+v", o)
	}
	if o := call(S.ctrl, protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: S.id}); !o.refused() {
		t.Errorf("the last super admin was removed: %+v", o)
	}
	if o := role(S, A, domain.RoleSuperAdmin); o.refused() {
		t.Errorf("S promotes A to super admin: %+v", o)
		return
	}
	if o := role(S, A, domain.RoleAdmin); o.refused() {
		t.Errorf("S demotes A, one of two super admins: %+v", o)
	}
}
