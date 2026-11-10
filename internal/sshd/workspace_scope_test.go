package sshd

import (
	"context"
	"encoding/json"
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
	signers       map[string]ssh.Signer
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
	signers["S"] = e.signer
	w.signers = signers
	w.clients = map[string]*protocol.Client{}
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
	echo := strings.NewReplacer(string(w.wsB.ID), "<id>", string(w.runB.ID), "<id>", string(w.runA.ID), "<id>", noWorkspace, "<id>", noRun, "<id>")
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
		"A": sorted(w.runUA.ID),
		"U": sorted(w.runUA.ID),
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
	if got := runs("A", protocol.RunListParams{WorkspaceID: a}); !slices.Equal(got, sorted(w.runUA.ID)) {
		t.Errorf("A run.list of wsA = %v, want only %s, not the run with a repository in wsB", got, w.runUA.ID)
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

	for _, r := range []*domain.Run{w.runUA, w.runB} {
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

// A refused call naming a workspace its caller cannot reach is audited
// without it, so N can neither write into B's audit view nor learn B's
// ids from it.
func TestAuditLeavesOutTheUnreachableWorkspaceOfARefusedCall(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	if err := w.call(t, "N", protocol.MethodWorkspaceAccessRevoke, protocol.WorkspaceAccessRevokeParams{
		WorkspaceID: string(w.wsB.ID), MemberID: string(w.u.ID),
	}, nil); err == nil {
		t.Fatal("N workspace.access.revoke in wsB succeeded")
	}
	entries, err := w.e.store.(store.AuditStore).ListAudit(t.Context(), store.AuditQuery{Member: w.n.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Method != protocol.MethodWorkspaceAccessRevoke || entries[0].WorkspaceID != "" {
		t.Fatalf("N's audit entries = %+v, want one revoke without a workspace", entries)
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
	// A run in a workspace U reaches, with an extra repository in one U
	// does not, does not exist for U.
	read := func(run, repo string) protocol.FilesReadParams {
		return protocol.FilesReadParams{WorkspaceID: string(w.wsA.ID), RunID: run, Repo: repo, Path: "README.md"}
	}
	runA := string(w.runA.ID)
	w.sameAsMissing(t, "U", protocol.MethodFilesRead, read(runA, b), read(noRun, b))
	w.sameAsMissing(t, "U", protocol.MethodRunGet, protocol.RunIDParams{RunID: runA}, protocol.RunIDParams{RunID: noRun})
	w.sameAsMissing(t, "U", protocol.MethodRunPull, protocol.RunPullParams{RunID: runA, Repo: b}, protocol.RunPullParams{RunID: noRun, Repo: b})
	w.sameAsMissing(t, "U", protocol.MethodChangesList, protocol.ChangesListParams{RunID: runA}, protocol.ChangesListParams{RunID: noRun})
	if err := w.call(t, "S", protocol.MethodFilesRead, read(runA, b), nil); err != nil {
		t.Fatalf("S files.read of the extra repository: %v", err)
	}
	for _, method := range []string{protocol.MethodWorkspaceTimeline, protocol.MethodCostReport, protocol.MethodTemplateList} {
		if err := w.call(t, "B", method, protocol.WorkspaceGetParams{WorkspaceID: b}, nil); err != nil {
			t.Errorf("B %s of their own workspace: %v", method, err)
		}
	}
}

func TestScheduleReplaceAndDeleteNeedItsCreatorOrTheWorkspaceAdmin(t *testing.T) {
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
	if err := w.call(t, "N", protocol.MethodScheduleSave, schedule, nil); err == nil || wireErrOf(t, err).Code != protocol.CodeDenied {
		t.Fatalf("another collaborator's schedule.save over it = %v, want denied", err)
	}
	if err := w.call(t, "U", protocol.MethodScheduleSave, schedule, nil); err != nil {
		t.Fatalf("the creator's schedule.save over it: %v", err)
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

// A session with an extra repository in a workspace the caller cannot
// reach does not exist for them: it is not listed or read, and its events,
// pull requests and status reason stay unseen. The figures insights
// aggregates still count it.
func TestASessionWithAnUnreachableRepoDoesNotExist(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := t.Context()
	runA := w.runA
	const url, reason = "https://github.com/acme/b/pull/7", "asked: which deploy key opens wsB?"
	now := time.Now()
	if _, err := w.e.db.UpdateRunStatus(ctx, runA.ID, domain.RunCompleted, reason, "", nil, &now); err != nil {
		t.Fatal(err)
	}
	if err := w.e.db.RecordRunPullRequest(ctx, &store.RunPullRequest{RunID: runA.ID, RepoWorkspace: w.wsB.ID, Repository: "acme/b", Number: 7,
		URL: url, State: "open", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []events.Payload{events.RunStatusPayload{To: domain.RunCompleted, Reason: reason}, events.RunDiffPayload{Repo: w.wsB.ID},
		events.RunPRPayload{RunID: runA.ID, Repo: w.wsB.ID, Number: 7, URL: url}} {
		if _, err := w.bus.Publish(ctx, events.Event{WorkspaceID: w.wsA.ID, RunID: runA.ID, Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	var got protocol.RunResult
	if err := w.call(t, "S", protocol.MethodRunGet, protocol.RunIDParams{RunID: string(runA.ID)}, &got); err != nil {
		t.Fatalf("S gets A's run: %v", err)
	}
	if len(got.Run.Repos) != 2 || len(got.PullRequests) != 1 {
		t.Fatalf("S sees repos %+v and pull requests %+v, want the primary, B's, and B's pull request", got.Run.Repos, got.PullRequests)
	}
	closeParams := func(id string) protocol.RunCloseParams {
		return protocol.RunCloseParams{RunID: id, Outcome: string(domain.RunAbandoned)}
	}
	from, to := now.Add(-time.Hour).UTC().Format(time.RFC3339), now.Add(time.Hour).UTC().Format(time.RFC3339)
	calls := []struct {
		method string
		params any
	}{
		{protocol.MethodRunList, protocol.RunListParams{}},
		{protocol.MethodRunList, protocol.RunListParams{WorkspaceID: string(w.wsA.ID)}},
		{protocol.MethodRunList, protocol.RunListParams{MemberID: string(w.a.ID)}},
		{protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{WorkspaceID: string(w.wsA.ID)}},
		{protocol.MethodChangesList, protocol.ChangesListParams{WorkspaceID: string(w.wsA.ID)}},
		{protocol.MethodInsightsGet, protocol.InsightsParams{Scope: protocol.InsightsScopeEveryone, From: from, To: to, TimeZone: "UTC"}},
	}
	for _, who := range []string{"S", "A", "U"} {
		if who != "S" {
			w.sameAsMissing(t, who, protocol.MethodRunGet, protocol.RunIDParams{RunID: string(runA.ID)}, protocol.RunIDParams{RunID: noRun})
			w.sameAsMissing(t, who, protocol.MethodRunClose, closeParams(string(runA.ID)), closeParams(noRun))
			var check protocol.RunCloseCheckResult
			if err := w.call(t, who, protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: []string{string(runA.ID), noRun}}, &check); err != nil {
				t.Fatalf("%s run.close_check: %v", who, err)
			}
			if got, missing := check.Runs[0].Error, check.Runs[1].Error; got == nil || missing == nil || got.Code != missing.Code || got.Message != missing.Message {
				t.Fatalf("%s run.close_check: unreachable = %+v, missing = %+v; want them identical", who, got, missing)
			}
		}
		for _, c := range calls {
			var res json.RawMessage
			if err := w.call(t, who, c.method, c.params, &res); err != nil {
				t.Fatalf("%s %s: %v", who, c.method, err)
			}
			leaked := false
			for _, secret := range []string{url, reason, string(w.wsB.ID), string(runA.ID)} {
				leaked = leaked || strings.Contains(string(res), secret)
			}
			if c.method == protocol.MethodInsightsGet {
				var insights protocol.InsightsResult
				if err := json.Unmarshal(res, &insights); err != nil {
					t.Fatal(err)
				}
				leaked = leaked || insights.PullRequests.Opened != 0
				if who == "A" && insights.Totals.Launched != 2 {
					t.Errorf("A insights launched = %d, want wsA's 2 runs counted", insights.Totals.Launched)
				}
			}
			if leaked != (who == "S") {
				t.Errorf("%s %s %+v shows A's run = %v, want %v: %s", who, c.method, c.params, leaked, who == "S", res)
			}
		}
	}
}

// A session's extra repositories are part of it from creation, so one still
// provisioning, or whose checkout failed, is no more visible than one running.
func TestASessionBeforeItsCheckoutHasItsWholeScope(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	for _, status := range []domain.RunStatus{domain.RunProvisioning, domain.RunFailed} {
		r := &domain.Run{WorkspaceID: w.wsA.ID, MemberID: w.a.ID, Task: "private " + string(status), Harness: "claude", Mode: domain.LaunchTUI,
			Status: status, Extras: []domain.RunRepo{{WorkspaceID: w.wsB.ID, Name: "b", Path: "/repos/b", BaseBranch: "main"}}}
		if err := w.e.db.CreateRun(t.Context(), r); err != nil {
			t.Fatal(err)
		}
		got := w.call(t, "U", protocol.MethodRunGet, protocol.RunIDParams{RunID: string(r.ID)}, nil)
		missing := w.call(t, "U", protocol.MethodRunGet, protocol.RunIDParams{RunID: noRun}, nil)
		if got == nil || missing == nil || wireErrOf(t, got).Code != wireErrOf(t, missing).Code ||
			strings.ReplaceAll(wireErrOf(t, got).Message, string(r.ID), noRun) != wireErrOf(t, missing).Message {
			t.Fatalf("U run.get of a %s run with a wsB repo = %v, missing = %v; want them identical", status, got, missing)
		}
		var list json.RawMessage
		if err := w.call(t, "U", protocol.MethodRunList, protocol.RunListParams{WorkspaceID: string(w.wsA.ID)}, &list); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(list), string(r.ID)) {
			t.Fatalf("U run.list shows the %s run with a wsB repo: %s", status, list)
		}
		if err := w.call(t, "S", protocol.MethodRunGet, protocol.RunIDParams{RunID: string(r.ID)}, nil); err != nil {
			t.Fatalf("S run.get of the %s run: %v", status, err)
		}
	}
}

// A coordination message between two sessions of one workspace names its
// recipient, so it reaches only readers who reach the recipient's every
// workspace too.
func TestAMessageToASessionWithAnUnreachableRepoStaysUnseen(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := t.Context()
	publish := func(to domain.RunID) uint64 {
		t.Helper()
		ev, err := w.bus.Publish(ctx, events.Event{WorkspaceID: w.wsA.ID, RunID: w.runUA.ID, Payload: events.CoordMessagePayload{
			MessageID: "msg-" + string(to), WorkspaceID: w.wsA.ID, FromRunID: w.runUA.ID, ToRunID: to, Kind: "message",
		}})
		if err != nil {
			t.Fatal(err)
		}
		return ev.Seq
	}
	plain := publish(w.runUA.ID)
	hidden := publish(w.runA.ID)
	for who, want := range map[string][]uint64{"S": {plain, hidden}, "A": {plain}, "U": {plain}} {
		var timeline protocol.WorkspaceTimelineResult
		if err := w.call(t, who, protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{WorkspaceID: string(w.wsA.ID)}, &timeline); err != nil {
			t.Fatalf("%s timeline: %v", who, err)
		}
		var changes protocol.ChangesListResult
		if err := w.call(t, who, protocol.MethodChangesList, protocol.ChangesListParams{Cursor: "0", WorkspaceID: string(w.wsA.ID)}, &changes); err != nil {
			t.Fatalf("%s changes.list: %v", who, err)
		}
		var fromTimeline, fromChanges []uint64
		for _, ev := range timeline.Events {
			if ev.Seq == plain || ev.Seq == hidden {
				fromTimeline = append(fromTimeline, ev.Seq)
			}
		}
		for _, c := range changes.Changes {
			if c.Seq == plain || c.Seq == hidden {
				fromChanges = append(fromChanges, c.Seq)
			}
		}
		slices.Sort(fromTimeline)
		slices.Sort(fromChanges)
		if !slices.Equal(fromTimeline, want) || !slices.Equal(fromChanges, want) {
			t.Errorf("%s coord.message events: timeline %v, changes %v, want %v", who, fromTimeline, fromChanges, want)
		}
	}
}

// A message from a session in a workspace the caller cannot reach is
// counted in a close check but not named.
func TestForeignSendersStayInTheirWorkspace(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ua := w.runUA
	if err := w.e.db.AppendRunMessage(t.Context(), &store.RunMessage{WorkspaceID: w.wsB.ID, FromRun: w.runB.ID, ToRun: ua.ID, Body: "hi",
		Kind: store.RunMessageKindMessage, CreatedAt: time.Now()}, 100); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"S", "A", "U"} {
		var res json.RawMessage
		if err := w.call(t, who, protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: []string{string(ua.ID)}}, &res); err != nil {
			t.Fatalf("%s run.close_check: %v", who, err)
		}
		if leaked := strings.Contains(string(res), string(w.runB.ID)); leaked != (who == "S") {
			t.Errorf("%s run.close_check names wsB's sender = %v, want %v: %s", who, leaked, who == "S", res)
		}
		if !strings.Contains(string(res), `"count":1`) {
			t.Errorf("%s run.close_check = %s, want the message counted", who, res)
		}
	}
}

// An event naming a session that no longer exists reaches only super
// admins, unless it names every workspace of that session itself, as
// run.deleted does.
func TestADeletedSessionsEventsNeedEveryWorkspace(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := t.Context()
	publish := func(r *domain.Run, p events.Payload) uint64 {
		t.Helper()
		ev, err := w.bus.Publish(ctx, events.Event{WorkspaceID: r.WorkspaceID, RunID: r.ID, Payload: p})
		if err != nil {
			t.Fatal(err)
		}
		return ev.Seq
	}
	plain := publish(w.runUA, events.RunStatusPayload{To: domain.RunCompleted, Reason: "plain"})
	multi := publish(w.runA, events.RunStatusPayload{To: domain.RunCompleted, Reason: "two repos"})
	for _, r := range []*domain.Run{w.runUA, w.runA} {
		if err := w.e.db.DeleteRun(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	legacy := publish(w.runUA, events.RunDeletedPayload{})
	plainDeleted := publish(w.runUA, events.RunDeletedPayload{ExtraWorkspaceIDs: []domain.WorkspaceID{}})
	multiDeleted := publish(w.runA, events.RunDeletedPayload{ExtraWorkspaceIDs: []domain.WorkspaceID{w.wsB.ID}})
	all := []uint64{plain, multi, legacy, plainDeleted, multiDeleted}
	seqs := func(who, method string, params any) []uint64 {
		t.Helper()
		var out []uint64
		switch method {
		case protocol.MethodChangesList:
			var res protocol.ChangesListResult
			if err := w.call(t, who, method, params, &res); err != nil {
				t.Fatalf("%s %s: %v", who, method, err)
			}
			for _, c := range res.Changes {
				out = append(out, c.Seq)
			}
		case protocol.MethodWorkspaceTimeline:
			var res protocol.WorkspaceTimelineResult
			if err := w.call(t, who, method, params, &res); err != nil {
				t.Fatalf("%s %s: %v", who, method, err)
			}
			for _, ev := range res.Events {
				out = append(out, ev.Seq)
			}
		}
		slices.Sort(out)
		return slices.DeleteFunc(out, func(seq uint64) bool { return !slices.Contains(all, seq) })
	}
	for who, want := range map[string][]uint64{
		"S": all,
		"A": {plainDeleted},
		"U": {plainDeleted},
	} {
		for _, c := range []struct {
			method string
			params any
		}{
			{protocol.MethodChangesList, protocol.ChangesListParams{Cursor: "0", WorkspaceID: string(w.wsA.ID)}},
			{protocol.MethodChangesList, protocol.ChangesListParams{Cursor: "0"}},
			{protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{WorkspaceID: string(w.wsA.ID)}},
		} {
			if got := seqs(who, c.method, c.params); !slices.Equal(got, want) {
				t.Errorf("%s %s %+v = %v, want %v", who, c.method, c.params, got, want)
			}
		}
	}
}
