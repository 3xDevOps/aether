package sshd

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/approvals"
	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/templates"
	"github.com/3xDevOps/Aether/internal/timeline"
)

// makeSuperAdmin promotes the env's seeded admin, for tests of what only a
// super admin sees.
func makeSuperAdmin(t *testing.T, e *testEnv) {
	t.Helper()
	setRole(t, e, domain.RoleSuperAdmin)
}

func setRole(t *testing.T, e *testEnv, role domain.Role) {
	t.Helper()
	m, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.Role = role
	if err := e.db.UpdateMember(t.Context(), m); err != nil {
		t.Fatal(err)
	}
}

type fixedOverlaps []overlap.Entry

func (f fixedOverlaps) Overlaps(context.Context) ([]overlap.Entry, error) { return f, nil }

// scopeWorld is the threat-model cast: super admin S, admin A owning wsA,
// admin B owning wsB, collaborator U granted wsA, and N with no grant.
type scopeWorld struct {
	e          *testEnv
	bus        *events.InProc
	wsA, wsB   *domain.Workspace
	runA, runB *domain.Run
	// runUA and runUB are U's finished runs; U lost wsB after launching
	// runUB there.
	runUA, runUB  *domain.Run
	a, b, u, n    *domain.Member
	clients       map[string]*protocol.Client
	runOverlapsOf fixedOverlaps
}

func newScopeWorld(t *testing.T) *scopeWorld {
	t.Helper()
	ctx := context.Background()
	w := &scopeWorld{}
	w.e = newScopedTestEnv(t, func(c *Config) {
		dir := filepath.Dir(filepath.Dir(c.HostKeyPath))
		log, err := events.OpenSQLiteLog(filepath.Join(dir, "events.db"))
		if err != nil {
			t.Fatal(err)
		}
		if w.bus, err = events.NewInProc(ctx, log); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = w.bus.Close()
			_ = log.Close()
		})
		c.Bus = w.bus
		c.Services.Timeline = timeline.NewReader(log)
		presence, err := approvals.New(approvals.Config{Bus: c.Bus})
		if err != nil {
			t.Fatal(err)
		}
		if err = presence.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = presence.Close() })
		c.Services.Approvals = presence
		costs, err := cost.New(cost.Config{Store: c.Store, Bus: c.Bus})
		if err != nil {
			t.Fatal(err)
		}
		c.Services.Costs = costs
		tmpl, err := templates.New(templates.Config{Store: c.Store, Bus: c.Bus, Runs: c.Runs.(templates.Launcher), Interval: time.Hour, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		c.Services.Templates = tmpl
		c.Services.Files = &fakeFiles{}
		c.Services.Overlaps = &w.runOverlapsOf
	})
	e := w.e
	signers := map[string]ssh.Signer{}
	member := func(name string, role domain.Role) *domain.Member {
		signer, m := addMember(t, e, name, role, false)
		signers[name] = signer
		return m
	}
	w.a, w.b = member("A", domain.RoleAdmin), member("B", domain.RoleAdmin)
	w.u, w.n = member("U", domain.RoleCollaborator), member("N", domain.RoleCollaborator)
	workspace := func(name string, owner domain.MemberID) *domain.Workspace {
		ws := &domain.Workspace{Name: name, BaseBranch: "main", OwnerID: owner}
		if err := e.db.CreateWorkspace(ctx, ws); err != nil {
			t.Fatal(err)
		}
		return ws
	}
	w.wsA, w.wsB = workspace("a", w.a.ID), workspace("b", w.b.ID)
	if err := e.db.GrantWorkspace(ctx, w.wsA.ID, w.u.ID, w.a.ID); err != nil {
		t.Fatal(err)
	}
	run := func(ws *domain.Workspace, owner domain.MemberID, status domain.RunStatus) *domain.Run {
		r := &domain.Run{WorkspaceID: ws.ID, MemberID: owner, Task: "task in " + ws.Name, Harness: "claude", Mode: domain.LaunchTUI,
			Status: status}
		if err := e.db.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.db.SetRunReview(ctx, r.ID, domain.ReviewReady, ""); err != nil {
			t.Fatal(err)
		}
		return r
	}
	w.runA, w.runB = run(w.wsA, w.a.ID, domain.RunRunning), run(w.wsB, w.b.ID, domain.RunRunning)
	w.runUA, w.runUB = run(w.wsA, w.u.ID, domain.RunCompleted), run(w.wsB, w.u.ID, domain.RunCompleted)
	if err := e.db.SetRunRepos(ctx, w.runA.ID, []domain.RunRepo{{WorkspaceID: w.wsB.ID, Name: "b", Path: "/repos/b", Branch: "aether/b", BaseBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	w.runOverlapsOf = fixedOverlaps{
		{RunID: w.runB.ID, With: []overlap.Peer{{RunID: w.runUB.ID, MemberID: w.u.ID, Files: []string{"x.go"}}}},
		{RunID: w.runUB.ID, With: []overlap.Peer{{RunID: w.runB.ID, MemberID: w.b.ID, Files: []string{"x.go"}}}},
	}
	w.clients = map[string]*protocol.Client{"S": controlClient(t, e)}
	for name, signer := range signers {
		w.clients[name] = controlAs(t, e, signer)
	}
	return w
}

const noWorkspace, noRun = "ws-doesnotexist", "run-doesnotexist"

func (w *scopeWorld) call(t *testing.T, who, method string, params, out any) error {
	t.Helper()
	return w.clients[who].Call(method, params, out)
}

// sameAsMissing asserts that calling method on an existing id the caller
// cannot reach fails exactly as on an id that does not exist.
func (w *scopeWorld) sameAsMissing(t *testing.T, who, method string, unreachable, missing any) {
	t.Helper()
	got := w.call(t, who, method, unreachable, nil)
	want := w.call(t, who, method, missing, nil)
	if got == nil || want == nil {
		t.Fatalf("%s %s: unreachable = %v, missing = %v; want both to fail", who, method, got, want)
	}
	g, m := wireErrOf(t, got), wireErrOf(t, want)
	// An error may echo the id it was given.
	echo := strings.NewReplacer(string(w.wsB.ID), "<id>", string(w.runB.ID), "<id>", noWorkspace, "<id>", noRun, "<id>")
	if g.Code != m.Code || echo.Replace(g.Message) != echo.Replace(m.Message) || string(g.Data) != string(m.Data) {
		t.Fatalf("%s %s: unreachable = %d %q, missing = %d %q; want them identical", who, method, g.Code, g.Message, m.Code, m.Message)
	}
}

func TestWorkspaceScopeLists(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	e := w.e

	roles := func(who string) map[string]string {
		t.Helper()
		var res protocol.WorkspaceListResult
		if err := w.call(t, who, protocol.MethodWorkspaceList, struct{}{}, &res); err != nil {
			t.Fatalf("%s workspace.list: %v", who, err)
		}
		out := map[string]string{}
		for _, ws := range res.Workspaces {
			out[ws.ID] = ws.Role
		}
		return out
	}
	a, b, seeded := string(w.wsA.ID), string(w.wsB.ID), string(e.ws.ID)
	for who, want := range map[string]map[string]string{
		"S": {seeded: "admin", a: "admin", b: "admin"},
		"A": {a: "admin"},
		"B": {b: "admin"},
		"U": {a: "collaborator"},
		"N": {},
	} {
		if got := roles(who); !maps.Equal(got, want) {
			t.Errorf("%s workspace.list roles = %v, want %v", who, got, want)
		}
	}

	runs := func(who string, p protocol.RunListParams) []string {
		t.Helper()
		var res protocol.RunListResult
		if err := w.call(t, who, protocol.MethodRunList, p, &res); err != nil {
			t.Fatalf("%s run.list %+v: %v", who, p, err)
		}
		var ids []string
		for _, r := range res.Runs {
			ids = append(ids, r.ID)
			for _, repo := range r.Repos {
				if !repo.Primary && who != "S" && repo.WorkspaceID == b {
					t.Errorf("%s run.list shows %s's extra repository in wsB", who, r.ID)
				}
			}
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...domain.RunID) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, string(id))
		}
		slices.Sort(out)
		return out
	}
	for who, want := range map[string][]string{
		"S": sorted(e.run.ID, w.runA.ID, w.runB.ID, w.runUA.ID, w.runUB.ID),
		"A": sorted(w.runA.ID, w.runUA.ID),
		"U": sorted(w.runA.ID, w.runUA.ID),
		"N": sorted(),
	} {
		for _, p := range []protocol.RunListParams{{}, {ActiveOnly: true}, {MemberID: string(w.u.ID)}} {
			got := runs(who, p)
			keep := []string{}
			for _, id := range want {
				r, err := e.db.GetRun(t.Context(), domain.RunID(id))
				if err != nil {
					t.Fatal(err)
				}
				if (!p.ActiveOnly || !r.Status.Terminal()) && (p.MemberID == "" || string(r.MemberID) == p.MemberID) {
					keep = append(keep, id)
				}
			}
			if !slices.Equal(got, keep) {
				t.Errorf("%s run.list %+v = %v, want %v", who, p, got, keep)
			}
		}
	}
	if got := runs("A", protocol.RunListParams{WorkspaceID: b}); len(got) != 0 {
		t.Errorf("A run.list of wsB = %v, want none, as for a workspace that does not exist", got)
	}

	attention := func(who string) []string {
		t.Helper()
		var res protocol.AttentionListResult
		if err := w.call(t, who, protocol.MethodAttentionList, protocol.AttentionListParams{}, &res); err != nil {
			t.Fatalf("%s attention.list: %v", who, err)
		}
		var ids []string
		for _, it := range res.Items {
			ids = append(ids, it.RunID)
		}
		return ids
	}
	if got := attention("U"); !slices.Equal(got, []string{string(w.runUA.ID)}) {
		t.Errorf("U attention.list = %v, want only %s, not the run in the workspace U lost", got, w.runUA.ID)
	}

	from, to := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	insights := func(who string) protocol.InsightsResult {
		t.Helper()
		var res protocol.InsightsResult
		if err := w.call(t, who, protocol.MethodInsightsGet, protocol.InsightsParams{Scope: protocol.InsightsScopeEveryone, From: from, To: to, TimeZone: "UTC"}, &res); err != nil {
			t.Fatalf("%s insights.get: %v", who, err)
		}
		return res
	}
	members := func(res protocol.InsightsResult) []string {
		var ids []string
		for _, m := range res.Outcomes.Members {
			ids = append(ids, m.MemberID)
		}
		slices.Sort(ids)
		return ids
	}
	if got := insights("N"); got.Totals.Launched != 0 || len(got.ByWorkspace) != 0 || len(got.Outcomes.Members) != 0 || got.NeedsYou != 0 {
		t.Errorf("N insights = launched %d, workspaces %+v, members %+v, needs you %d; want nothing", got.Totals.Launched, got.ByWorkspace, got.Outcomes.Members, got.NeedsYou)
	}
	gotA := insights("A")
	if wantMembers := sorted(domain.RunID(w.a.ID), domain.RunID(w.u.ID)); gotA.Totals.Launched != 2 || len(gotA.ByWorkspace) != 1 ||
		gotA.ByWorkspace[0].WorkspaceID != a || !slices.Equal(members(gotA), wantMembers) {
		t.Errorf("A insights = launched %d, workspaces %+v, members %v; want wsA's 2 runs and members %v", gotA.Totals.Launched, gotA.ByWorkspace, members(gotA), wantMembers)
	}
	if got := insights("S"); got.Totals.Launched != 5 || len(got.Outcomes.Members) != 5 {
		t.Errorf("S insights = launched %d, %d members; want every run and member", got.Totals.Launched, len(got.Outcomes.Members))
	}

	for _, r := range []*domain.Run{w.runA, w.runB} {
		if _, err := w.bus.Publish(t.Context(), events.Event{WorkspaceID: r.WorkspaceID, RunID: r.ID, Payload: events.RunStatusPayload{To: domain.RunRunning}}); err != nil {
			t.Fatal(err)
		}
	}
	changes := func(who string) map[string]bool {
		t.Helper()
		var res protocol.ChangesListResult
		if err := w.call(t, who, protocol.MethodChangesList, protocol.ChangesListParams{Cursor: "0"}, &res); err != nil {
			t.Fatalf("%s changes.list: %v", who, err)
		}
		out := map[string]bool{}
		for _, c := range res.Changes {
			out[c.WorkspaceID] = true
		}
		return out
	}
	if got := changes("A"); !maps.Equal(got, map[string]bool{a: true}) {
		t.Errorf("A changes.list workspaces = %v, want only wsA", got)
	}
	if got := changes("N"); len(got) != 0 {
		t.Errorf("N changes.list workspaces = %v, want none", got)
	}
	if got := changes("S"); !maps.Equal(got, map[string]bool{a: true, b: true}) {
		t.Errorf("S changes.list workspaces = %v, want both", got)
	}

	overlaps := func(who string) int {
		t.Helper()
		var res protocol.RunOverlapsResult
		if err := w.call(t, who, protocol.MethodRunOverlaps, struct{}{}, &res); err != nil {
			t.Fatalf("%s run.overlaps: %v", who, err)
		}
		return len(res.Overlaps)
	}
	if got := overlaps("A"); got != 0 {
		t.Errorf("A run.overlaps = %d entries, want none of wsB's", got)
	}
	if got := overlaps("B"); got != 2 {
		t.Errorf("B run.overlaps = %d entries, want wsB's 2", got)
	}

	if err := w.call(t, "B", protocol.MethodPresenceHeartbeat, protocol.PresenceHeartbeatParams{WorkspaceID: b}, nil); err != nil {
		t.Fatalf("B presence.heartbeat: %v", err)
	}
	roster := func(who string, p protocol.PresenceRosterParams) []string {
		t.Helper()
		var res protocol.PresenceRosterResult
		if err := w.call(t, who, protocol.MethodPresenceRoster, p, &res); err != nil {
			t.Fatalf("%s presence.roster: %v", who, err)
		}
		var ids []string
		for _, m := range res.Members {
			ids = append(ids, m.MemberID)
		}
		return ids
	}
	for _, p := range []protocol.PresenceRosterParams{{}, {WorkspaceID: b}} {
		if got := roster("A", p); slices.Contains(got, string(w.b.ID)) {
			t.Errorf("A presence.roster %+v = %v, want B's presence in wsB left out", p, got)
		}
		if got := roster("S", p); !slices.Contains(got, string(w.b.ID)) {
			t.Errorf("S presence.roster %+v = %v, want B present", p, got)
		}
	}

	as := e.store.(store.AuditStore)
	for _, entry := range []*store.AuditEntry{
		{Member: w.b.ID, Method: "run.kill", WorkspaceID: b},
		{Member: w.u.ID, Method: "run.kill", WorkspaceID: a},
		{Member: w.a.ID, Method: "connector.revoke"},
		{Member: w.n.ID, Method: "connector.revoke"},
	} {
		entry.At, entry.Via = time.Now(), ViaConnector
		if err := as.AddAudit(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
	}
	audit := func(who string) []string {
		t.Helper()
		var res protocol.AuditListResult
		if err := w.call(t, who, protocol.MethodAuditList, protocol.AuditListParams{}, &res); err != nil {
			t.Fatalf("%s audit.list: %v", who, err)
		}
		var ids []string
		for _, en := range res.Entries {
			ids = append(ids, en.MemberID)
		}
		slices.Sort(ids)
		return ids
	}
	for who, want := range map[string][]string{
		"S": sorted(domain.RunID(w.a.ID), domain.RunID(w.b.ID), domain.RunID(w.u.ID), domain.RunID(w.n.ID)),
		"A": sorted(domain.RunID(w.a.ID), domain.RunID(w.u.ID)),
		"U": sorted(domain.RunID(w.u.ID)),
		"N": sorted(domain.RunID(w.n.ID)),
	} {
		if got := audit(who); !slices.Equal(got, want) {
			t.Errorf("%s audit.list members = %v, want %v", who, got, want)
		}
	}
}

func TestWorkspaceScopeUnreachableReadsAsMissing(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	b := string(w.wsB.ID)
	for _, who := range []string{"A", "U", "N"} {
		w.sameAsMissing(t, who, protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: b}, protocol.WorkspaceGetParams{WorkspaceID: noWorkspace})
		w.sameAsMissing(t, who, protocol.MethodRunGet, protocol.RunIDParams{RunID: string(w.runB.ID)}, protocol.RunIDParams{RunID: noRun})
		w.sameAsMissing(t, who, protocol.MethodRunPull, protocol.RunPullParams{RunID: string(w.runB.ID)}, protocol.RunPullParams{RunID: noRun})
		w.sameAsMissing(t, who, protocol.MethodAttentionList, protocol.AttentionListParams{WorkspaceID: b}, protocol.AttentionListParams{WorkspaceID: noWorkspace})
		w.sameAsMissing(t, who, protocol.MethodInsightsGet,
			protocol.InsightsParams{WorkspaceID: b, Scope: protocol.InsightsScopeMine, From: "2026-01-01T00:00:00Z", To: "2026-01-02T00:00:00Z", TimeZone: "UTC"},
			protocol.InsightsParams{WorkspaceID: noWorkspace, Scope: protocol.InsightsScopeMine, From: "2026-01-01T00:00:00Z", To: "2026-01-02T00:00:00Z", TimeZone: "UTC"})
		w.sameAsMissing(t, who, protocol.MethodChangesList, protocol.ChangesListParams{WorkspaceID: b}, protocol.ChangesListParams{WorkspaceID: noWorkspace})
		w.sameAsMissing(t, who, protocol.MethodChangesList, protocol.ChangesListParams{RunID: string(w.runB.ID)}, protocol.ChangesListParams{RunID: noRun})
		w.sameAsMissing(t, who, protocol.MethodPresenceHeartbeat, protocol.PresenceHeartbeatParams{WorkspaceID: b}, protocol.PresenceHeartbeatParams{WorkspaceID: noWorkspace})
		w.sameAsMissing(t, who, protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{WorkspaceID: b}, protocol.WorkspaceTimelineParams{WorkspaceID: noWorkspace})
		w.sameAsMissing(t, who, protocol.MethodCostReport, protocol.CostReportParams{WorkspaceID: b}, protocol.CostReportParams{WorkspaceID: noWorkspace})
		w.sameAsMissing(t, who, protocol.MethodBudgetGet, protocol.BudgetGetParams{WorkspaceID: b}, protocol.BudgetGetParams{WorkspaceID: noWorkspace})
		w.sameAsMissing(t, who, protocol.MethodTemplateList, protocol.TemplateListParams{WorkspaceID: b}, protocol.TemplateListParams{WorkspaceID: noWorkspace})
		w.sameAsMissing(t, who, protocol.MethodScheduleList, protocol.ScheduleListParams{WorkspaceID: b}, protocol.ScheduleListParams{WorkspaceID: noWorkspace})
		var closeCheck protocol.RunCloseCheckResult
		if err := w.call(t, who, protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: []string{string(w.runB.ID), noRun}}, &closeCheck); err != nil {
			t.Fatalf("%s run.close_check: %v", who, err)
		}
		if got, missing := closeCheck.Runs[0].Error, closeCheck.Runs[1].Error; got == nil || missing == nil || got.Code != missing.Code || got.Message != missing.Message {
			t.Fatalf("%s run.close_check: unreachable = %+v, missing = %+v; want them identical", who, got, missing)
		}
	}
	// A run U reaches, with an extra repository in a workspace U does not.
	read := func(repo string) protocol.FilesReadParams {
		return protocol.FilesReadParams{WorkspaceID: string(w.wsA.ID), RunID: string(w.runA.ID), Repo: repo, Path: "README.md"}
	}
	w.sameAsMissing(t, "U", protocol.MethodFilesRead, read(b), read(noWorkspace))
	if err := w.call(t, "S", protocol.MethodFilesRead, read(b), nil); err != nil {
		t.Fatalf("S files.read of the extra repository: %v", err)
	}
	var pulled protocol.RunPullResult
	if err := w.call(t, "U", protocol.MethodRunPull, protocol.RunPullParams{RunID: string(w.runA.ID), Repo: b}, &pulled); err == nil {
		t.Fatalf("U run.pull of the extra repository in wsB = %+v, want refused", pulled)
	}
	for _, method := range []string{protocol.MethodWorkspaceTimeline, protocol.MethodCostReport, protocol.MethodTemplateList} {
		if err := w.call(t, "B", method, protocol.WorkspaceGetParams{WorkspaceID: b}, nil); err != nil {
			t.Errorf("B %s of their own workspace: %v", method, err)
		}
	}
}

func TestScheduleDeleteNeedsItsCreatorOrTheWorkspaceAdmin(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	a := string(w.wsA.ID)
	if err := w.e.db.GrantWorkspace(t.Context(), w.wsA.ID, w.n.ID, w.a.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.call(t, "A", protocol.MethodTemplateSave, protocol.TemplateSaveParams{
		WorkspaceID: a, Name: "nightly", Task: "upgrade deps", Harness: "claude", Mode: "headless",
	}, nil); err != nil {
		t.Fatalf("A template.save: %v", err)
	}
	schedule := protocol.ScheduleSaveParams{WorkspaceID: a, Template: "nightly", Cron: "0 3 * * *"}
	remove := protocol.ScheduleDeleteParams{WorkspaceID: a, Template: "nightly"}
	if err := w.call(t, "U", protocol.MethodScheduleSave, schedule, nil); err != nil {
		t.Fatalf("U schedule.save: %v", err)
	}
	if err := w.call(t, "N", protocol.MethodScheduleDelete, remove, nil); err == nil || wireErrOf(t, err).Code != protocol.CodeDenied {
		t.Fatalf("another collaborator's schedule.delete = %v, want denied", err)
	}
	if err := w.call(t, "A", protocol.MethodScheduleDelete, remove, nil); err != nil {
		t.Fatalf("the workspace admin's schedule.delete: %v", err)
	}
	if err := w.call(t, "U", protocol.MethodScheduleSave, schedule, nil); err != nil {
		t.Fatalf("U schedule.save again: %v", err)
	}
	if err := w.call(t, "U", protocol.MethodScheduleDelete, remove, nil); err != nil {
		t.Fatalf("the creator's schedule.delete: %v", err)
	}
}
