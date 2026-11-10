//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
	"github.com/3xDevOps/Aether/internal/ops"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
)

type probe struct {
	method string
	params func(id string) any
}

func templateOf(ws string, w *wsaEnv) string {
	if ws == w.wsB {
		return "access-B"
	}
	return "access-A"
}

// wsWrites are the by-workspace writes and admin calls, each as the
// params naming ws.
func (w *wsaEnv) wsWrites() []probe {
	one := 1
	return []probe{
		{protocol.MethodTemplateSave, func(ws string) any {
			return protocol.TemplateSaveParams{WorkspaceID: ws, Name: "access-probe", Task: "probe", Harness: "fake"}
		}},
		{protocol.MethodTemplateDelete, func(ws string) any {
			return protocol.TemplateDeleteParams{WorkspaceID: ws, Name: templateOf(ws, w)}
		}},
		{protocol.MethodTemplateLaunch, func(ws string) any {
			return protocol.TemplateLaunchParams{WorkspaceID: ws, Name: templateOf(ws, w)}
		}},
		{protocol.MethodScheduleSave, func(ws string) any {
			return protocol.ScheduleSaveParams{WorkspaceID: ws, Template: templateOf(ws, w), Cron: "0 3 * * *"}
		}},
		{protocol.MethodScheduleDelete, func(ws string) any {
			return protocol.ScheduleDeleteParams{WorkspaceID: ws, Template: templateOf(ws, w)}
		}},
		{protocol.MethodBudgetSet, func(ws string) any { return protocol.BudgetSetParams{WorkspaceID: ws, LimitUSD: 50} }},
		{protocol.MethodWorkspaceMirrorStatus, func(ws string) any { return protocol.WorkspaceMirrorParams{WorkspaceID: ws} }},
		{protocol.MethodWorkspaceMirrorConfigure, func(ws string) any {
			return protocol.WorkspaceMirrorConfigureParams{WorkspaceID: ws, SourceURL: "https://example.invalid/acme/app.git", Branch: "main", Auth: string(domain.MirrorAuthPublic)}
		}},
		{protocol.MethodWorkspaceMirrorRefresh, func(ws string) any { return protocol.WorkspaceMirrorParams{WorkspaceID: ws} }},
		{protocol.MethodWorkspaceMirrorDisable, func(ws string) any { return protocol.WorkspaceMirrorParams{WorkspaceID: ws} }},
		{protocol.MethodWorkspaceSettings, func(ws string) any {
			return protocol.WorkspaceSettingsParams{WorkspaceID: ws, SteerOthers: "admins_only"}
		}},
		{protocol.MethodWorkspaceEnvironmentGet, func(ws string) any { return protocol.WorkspaceEnvironmentGetParams{WorkspaceID: ws} }},
		{protocol.MethodWorkspaceEnvironmentSet, func(ws string) any {
			return protocol.WorkspaceEnvironmentSetParams{WorkspaceID: ws, Unset: []string{"ACCESS_PROBE"}}
		}},
		{protocol.MethodWorkspaceChildLimits, func(ws string) any {
			return protocol.WorkspaceChildLimitsParams{WorkspaceID: ws, MaxLive: &one}
		}},
		{protocol.MethodWorkspaceOrigin, func(ws string) any {
			return protocol.WorkspaceOriginParams{WorkspaceID: ws, Origin: "https://example.invalid/acme/app.git"}
		}},
		{protocol.MethodFilesWrite, func(ws string) any {
			return protocol.FilesWriteParams{WorkspaceID: ws, Path: "probe.txt", Content: "probe"}
		}},
		{protocol.MethodFilesTree, func(ws string) any { return protocol.FilesTreeParams{WorkspaceID: ws} }},
		{protocol.MethodMemoryAdd, func(ws string) any {
			return protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindConvention, Text: "probe note"}
		}},
		{protocol.MethodMemoryBrief, func(ws string) any { return protocol.MemoryBriefParams{WorkspaceID: ws} }},
		{protocol.MethodMemoryShow, func(ws string) any { return protocol.MemoryShowParams{WorkspaceID: ws, ID: "mem-0000000000"} }},
		{protocol.MethodWorkspaceAccessRevoke, func(ws string) any {
			return protocol.WorkspaceAccessRevokeParams{WorkspaceID: ws, MemberID: w.members["U"].id}
		}},
		{protocol.MethodWorkspaceDelete, func(ws string) any { return protocol.WorkspaceDeleteParams{WorkspaceID: ws} }},
	}
}

// runWrites are the by-run calls a session's controls make.
var runWrites = []probe{
	{protocol.MethodRunGitStatus, func(run string) any { return protocol.RunGitStatusParams{RunID: run} }},
	{protocol.MethodRunGitDiff, func(run string) any { return protocol.RunGitDiffParams{RunID: run} }},
	{protocol.MethodRunGitLog, func(run string) any { return protocol.RunGitLogParams{RunID: run} }},
	{protocol.MethodFilesDiff, func(run string) any { return protocol.FilesDiffParams{RunID: run, Path: "README.md"} }},
	{protocol.MethodTerminalHistory, func(run string) any { return protocol.TerminalHistoryParams{RunID: run} }},
	{protocol.MethodRunChildren, func(run string) any { return protocol.RunChildrenParams{RunID: run} }},
	{protocol.MethodRunInject, func(run string) any {
		return protocol.RunInjectParams{RunID: run, Message: "access probe", IdempotencyKey: "access-probe"}
	}},
	{protocol.MethodRunPause, func(run string) any { return protocol.RunIDParams{RunID: run} }},
	{protocol.MethodWatchCreate, func(run string) any {
		return protocol.WatchCreateParams{Target: protocol.WatchTarget{Kind: protocol.WatchTargetRun, RunID: run}, Label: "access probe"}
	}},
	{protocol.MethodDevTerminalList, func(run string) any {
		return protocol.DevTerminalListParams{DevRunParams: protocol.DevRunParams{RunID: run}}
	}},
	{protocol.MethodDevTerminalStart, func(run string) any {
		return protocol.DevTerminalStartParams{DevRunParams: protocol.DevRunParams{RunID: run}}
	}},
	{protocol.MethodDevPortList, func(run string) any {
		return protocol.DevPortListParams{DevRunParams: protocol.DevRunParams{RunID: run}}
	}},
}

// assertForeignWrites is threat cases 2 and 7 for the writes: a write
// naming an unreachable workspace or session is refused exactly as one
// naming an id that never existed, and a port forward to such a session
// is refused with a missing one's reason.
func (w *wsaEnv) assertForeignWrites(t *testing.T, name string) {
	m := w.members[name]
	for _, p := range w.wsWrites() {
		for _, ws := range w.foreignWorkspaces(name) {
			missing := missingID(ws)
			f := call(m.ctrl, p.method, p.params(ws))
			if !f.refused() {
				t.Errorf("%s %s of unreachable %s succeeded: %s", name, p.method, ws, f.Result)
			}
			assertLikeMissing(t, p.method, f, call(m.ctrl, p.method, p.params(missing)), ws, missing)
		}
	}
	for _, p := range runWrites {
		for _, run := range w.foreignRuns(name) {
			missing := missingID(run)
			f := call(m.ctrl, p.method, p.params(run))
			if !f.refused() {
				t.Errorf("%s %s of unreachable %s succeeded: %s", name, p.method, run, f.Result)
			}
			assertLikeMissing(t, p.method, f, call(m.ctrl, p.method, p.params(missing)), run, missing)
		}
	}
	forward := func(run string) string {
		ch, reqs, err := m.client.OpenChannel("direct-tcpip", ssh.Marshal(struct {
			DestHost string
			DestPort uint32
			OrigHost string
			OrigPort uint32
		}{"run:" + run, 8080, "127.0.0.1", 0}))
		if err == nil {
			go ssh.DiscardRequests(reqs)
			_ = ch.Close()
			return "opened"
		}
		return err.Error()
	}
	for _, run := range w.foreignRuns(name) {
		missing := missingID(run)
		if f, mi := forward(run), forward(missing); normalize(f, run, missing) != normalize(mi, run, missing) || f == "opened" {
			t.Errorf("port forward to unreachable %s = %q, to a missing session = %q", run, f, mi)
		}
	}
}

// assertWorkspaceRoles checks the workspace actions each member may take
// in the workspaces they reach: an admin there administers it, a
// collaborator launches and writes, a viewer only reads.
func (w *wsaEnv) assertWorkspaceRoles(t *testing.T, name string) {
	m := w.members[name]
	for ws, role := range w.roles[name] {
		want := func(method string, allowed bool, params any) {
			t.Helper()
			o := call(m.ctrl, method, params)
			switch {
			case allowed && o.refused():
				t.Errorf("%s (%s in %s) %s refused: %+v", name, role, ws, method, o)
			case !allowed && o.Code != protocol.CodeDenied:
				t.Errorf("%s (%s in %s) %s = %+v, want denied", name, role, ws, method, o)
			}
		}
		admin, writer := role == "admin", role != "viewer"
		probeTemplate := "access-probe-" + strings.ToLower(name)
		want(protocol.MethodTemplateSave, admin, protocol.TemplateSaveParams{WorkspaceID: ws, Name: probeTemplate, Task: "probe", Harness: "fake"})
		if admin {
			want(protocol.MethodTemplateDelete, true, protocol.TemplateDeleteParams{WorkspaceID: ws, Name: probeTemplate})
		}
		want(protocol.MethodBudgetSet, admin, protocol.BudgetSetParams{WorkspaceID: ws, LimitUSD: 100})
		want(protocol.MethodWorkspaceSettings, admin, protocol.WorkspaceSettingsParams{WorkspaceID: ws})
		want(protocol.MethodWorkspaceEnvironmentSet, admin, protocol.WorkspaceEnvironmentSetParams{WorkspaceID: ws, Unset: []string{"ACCESS_PROBE"}})
		want(protocol.MethodWorkspaceAccessList, admin, protocol.WorkspaceAccessParams{WorkspaceID: ws})
		want(protocol.MethodWorkspaceMirrorStatus, true, protocol.WorkspaceMirrorParams{WorkspaceID: ws})
		want(protocol.MethodWorkspaceEnvironmentGet, true, protocol.WorkspaceEnvironmentGetParams{WorkspaceID: ws})
		want(protocol.MethodBudgetGet, true, protocol.BudgetGetParams{WorkspaceID: ws})
		want(protocol.MethodTemplateList, true, protocol.TemplateListParams{WorkspaceID: ws})
		want(protocol.MethodMemoryBrief, true, protocol.MemoryBriefParams{WorkspaceID: ws})
		schedule := protocol.ScheduleSaveParams{WorkspaceID: ws, Template: templateOf(ws, w), Cron: "0 4 * * *"}
		want(protocol.MethodScheduleSave, writer, schedule)
		if writer {
			want(protocol.MethodScheduleDelete, true, protocol.ScheduleDeleteParams{WorkspaceID: ws, Template: templateOf(ws, w)})
		}
		want(protocol.MethodMemoryAdd, writer, protocol.MemoryAddParams{WorkspaceID: ws, Kind: protocol.MemoryKindGotcha, Text: "probe by " + name})
	}
}

// assertMemory is team memory per member: a search and a brief in a
// reachable workspace carry its notes and none of another's.
func (w *wsaEnv) assertMemory(t *testing.T, name string) {
	m := w.members[name]
	for ws := range w.roles[name] {
		res, ok := decode[protocol.MemorySearchResult](t, "memory.search", call(m.ctrl, protocol.MethodMemorySearch, protocol.MemorySearchParams{WorkspaceID: ws, All: true}))
		found := false
		for _, it := range res.Items {
			found = found || strings.Contains(it.Title+it.Body, "access-note-"+ws)
			if ok && it.WorkspaceID != ws {
				t.Errorf("%s memory.search of %s carries a note of %s", name, ws, it.WorkspaceID)
			}
		}
		if ok && !found {
			t.Errorf("%s memory.search of reachable %s = %+v, missing its note", name, ws, res.Items)
		}
		brief, ok := decode[protocol.MemoryBriefResult](t, "memory.brief", call(m.ctrl, protocol.MethodMemoryBrief, protocol.MemoryBriefParams{WorkspaceID: ws}))
		for _, other := range []string{w.wsA, w.wsB} {
			if ok && other != ws && strings.Contains(brief.Text, "access-note-"+other) {
				t.Errorf("%s memory.brief of %s carries a note of %s", name, ws, other)
			}
		}
	}
}

// assertCounts is threat case 1's counts: every total a member is shown
// counts only the sessions of the workspaces they reach.
func (w *wsaEnv) assertCounts(t *testing.T, name string) {
	m := w.members[name]
	for ws := range w.roles[name] {
		report, ok := decode[protocol.CostReportResult](t, "cost.report", call(m.ctrl, protocol.MethodCostReport, protocol.CostReportParams{WorkspaceID: ws}))
		for _, r := range report.Runs {
			if ok && !slices.Contains(w.runs[ws], r.RunID) && slices.Contains(w.foreignRuns(name), r.RunID) {
				t.Errorf("%s cost.report of %s counts unreachable %s", name, ws, r.RunID)
			}
		}
	}
	attention, ok := decode[protocol.AttentionListResult](t, "attention.list", call(m.ctrl, protocol.MethodAttentionList, protocol.AttentionListParams{}))
	if ok && name == "N" && len(attention.Items) != 0 {
		t.Errorf("N attention.list = %d items, want none", len(attention.Items))
	}
	ids := append(w.foreignRuns(name), w.reachableRuns(name)...)
	check, ok := decode[protocol.RunCloseCheckResult](t, "run.close_check", call(m.ctrl, protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: ids}))
	if !ok {
		return
	}
	missing, _ := decode[protocol.RunCloseCheckResult](t, "run.close_check", call(m.ctrl, protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: []string{missingID(w.runA)}}))
	for i, r := range check.Runs {
		foreign := slices.Contains(w.foreignRuns(name), ids[i])
		switch {
		case foreign && (r.Error == nil || len(missing.Runs) != 1 || missing.Runs[0].Error == nil || r.Error.Code != missing.Runs[0].Error.Code ||
			normalize(r.Error.Message, ids[i]) != normalize(missing.Runs[0].Error.Message, missingID(w.runA))):
			t.Errorf("%s run.close_check of unreachable %s = %+v, want the error of a missing session %+v", name, ids[i], r.Error, missing.Runs)
		case !foreign && r.Error != nil:
			t.Errorf("%s run.close_check of reachable %s = %+v", name, ids[i], r.Error)
		}
	}
	insights, ok := decode[protocol.InsightsResult](t, "insights.get", call(m.ctrl, protocol.MethodInsightsGet, w.insightsParams("")))
	if ok && name == "N" && (insights.NeedsYou != 0 || len(insights.Outcomes.Members) != 0 || len(insights.ByWorkspace) != 0) {
		t.Errorf("N insights = needs you %d, members %+v, workspaces %+v; want none", insights.NeedsYou, insights.Outcomes.Members, insights.ByWorkspace)
	}
}

// foreignMarks are strings that only data of a workspace name cannot
// reach carries.
func (w *wsaEnv) foreignMarks(name string) []string {
	var out []string
	for _, ws := range w.foreignWorkspaces(name) {
		out = append(out, ws, "access-note-"+ws, "access-event-"+ws)
	}
	return append(out, w.foreignRuns(name)...)
}

// toolOutcome is a tool call's error flag and its text and structured
// result, as the agent reads them.
func toolOutcome(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return true, "transport: " + err.Error()
	}
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	structured, _ := json.Marshal(res.StructuredContent)
	b.Write(structured)
	return res.IsError, b.String()
}

// assertTools is the connector and the assistant for one member: the
// tools that list and search show nothing of an unreachable workspace,
// and reading one of its sessions reads as a missing one.
func (w *wsaEnv) assertTools(t *testing.T, name string, cs *mcp.ClientSession) {
	offered := map[string]bool{}
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		offered[tool.Name] = true
	}
	reads := []struct {
		tool string
		args map[string]any
	}{
		{"aether_list_workspaces", nil},
		{"aether_list_sessions", map[string]any{"mine": false}},
		{"aether_needs_attention", nil},
		{"aether_activity_summary", map[string]any{"period": "week"}},
		{"aether_changes", nil},
		{"aether_overlaps", nil},
		{"search", map[string]any{"query": "hold"}},
	}
	for ws := range w.roles[name] {
		reads = append(reads, struct {
			tool string
			args map[string]any
		}{"aether_memory_search", map[string]any{"workspace_id": ws, "words": "access-note"}})
	}
	for _, r := range reads {
		if !offered[r.tool] {
			continue
		}
		failed, out := toolOutcome(t, cs, r.tool, r.args)
		if failed {
			t.Errorf("%s %s failed: %s", name, r.tool, out)
		}
		for _, mark := range w.foreignMarks(name) {
			if strings.Contains(out, mark) {
				t.Errorf("%s %s shows %s of an unreachable workspace: %s", name, r.tool, mark, out)
			}
		}
		var want []string
		switch r.tool {
		case "aether_list_workspaces":
			for ws := range w.roles[name] {
				want = append(want, ws)
			}
		case "aether_list_sessions":
			want = w.reachableRuns(name)
		}
		for _, id := range want {
			if !strings.Contains(out, id) {
				t.Errorf("%s %s does not show reachable %s: %s", name, r.tool, id, out)
			}
		}
	}
	for _, tool := range []string{"aether_list_sessions", "aether_get_session", "aether_session_history", "fetch", "aether_memory_search"} {
		if !offered[tool] {
			continue
		}
		args := func(id string) map[string]any {
			switch tool {
			case "fetch":
				return map[string]any{"id": id}
			case "aether_list_sessions", "aether_memory_search":
				return map[string]any{"workspace_id": id}
			}
			return map[string]any{"run_id": id}
		}
		ids := w.foreignRuns(name)
		if tool == "aether_list_sessions" || tool == "aether_memory_search" {
			ids = w.foreignWorkspaces(name)
		}
		for _, id := range ids {
			missing := missingID(id)
			ff, fo := toolOutcome(t, cs, tool, args(id))
			mf, mo := toolOutcome(t, cs, tool, args(missing))
			if ff != mf || normalize(fo, id, missing) != normalize(mo, id, missing) {
				t.Errorf("%s %s of unreachable %s = %v %s; of a missing id = %v %s", name, tool, id, ff, fo, mf, mo)
			}
		}
	}
	if !offered["aether_list_workspaces"] || !offered["aether_get_session"] {
		t.Errorf("%s tools = %v, want the list and get tools", name, offered)
	}
}

func mcpSession(t *testing.T, register func(*mcp.Server)) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "aether", Version: "v0"}, nil)
	register(srv)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "access-test", Version: "v0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// connectorSession is the MCP connector a member authorized with every
// scope: its tools call as the connector, through the server's
// connector-scoped control channel, as internal/connector serves them.
func (w *wsaEnv) connectorSession(t *testing.T, name string) *mcp.ClientSession {
	m := w.members[name]
	scopes := []string{sshd.ConnectorScopeRead, sshd.ConnectorScopeWrite, sshd.ConnectorScopeControl}
	local := w.srv.srv.ssh.ConnectorLocal(sshd.ConnectorGrant{Member: domain.MemberID(m.id)}, "con-access-"+name, "Access test", scopes)
	return mcpSession(t, func(srv *mcp.Server) {
		ops.RegisterConnector(srv, func(ctx context.Context, method string, params, out any) error {
			raw, err := json.Marshal(params)
			if err != nil {
				return err
			}
			if raw, err = tools.Prepare(m.id, method, raw); err != nil {
				return err
			}
			result, perr := local.Call(ctx, method, raw)
			if perr != nil {
				return perr
			}
			return json.Unmarshal(result, out)
		}, func(ops.Scope) bool { return true })
	})
}

// assistantSession is the member's built-in assistant: the MCP bridge it
// runs, over its tool socket, calling as the member's assistant.
func (w *wsaEnv) assistantSession(t *testing.T, name string) *mcp.ClientSession {
	m := w.members[name]
	sock := filepath.Join(shortTempDir(t), "assistant.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &tools.Server{Caller: w.srv.srv.ssh.AssistantLocal(domain.MemberID(m.id)), Member: m.id,
		Confirm: func(context.Context, protocol.AssistantConfirmation) error { return nil }}
	go func() { _ = srv.Serve(t.Context(), ln) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "access-assistant", Version: "v0"}, nil).Connect(t.Context(), bridgeTransport(t, sock), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func bridgeTransport(t *testing.T, sock string) mcp.Transport {
	t.Helper()
	toBridge, fromClient := io.Pipe()
	toClient, fromBridge := io.Pipe()
	go func() {
		_ = mcpbridge.Run(t.Context(), mcpbridge.Config{Socket: sock, Assistant: true, In: toBridge, Out: fromBridge})
	}()
	return &mcp.IOTransport{Reader: toClient, Writer: fromClient}
}

// assertAccountSharing is threat case 11's shared account: a launch on
// another member's account needs that member to reach the workspace too.
func (w *wsaEnv) assertAccountSharing(t *testing.T) {
	S, A, B, U, N := w.members["S"], w.members["A"], w.members["B"], w.members["U"], w.members["N"]
	shareCall := func(owner *wsaMember, with string) outcome {
		return call(owner.ctrl, protocol.MethodAccountShare, protocol.AccountMemberParams{MemberID: with})
	}
	f := shareCall(B, U.id)
	if !f.refused() {
		t.Errorf("B shared an account with U, whom B cannot see: %s", f.Result)
	}
	assertLikeMissing(t, "B account.share with U", f, shareCall(B, missingID(U.id)), U.id, missingID(U.id))
	access := func(method, ws string, m *wsaMember) {
		t.Helper()
		var err error
		if method == protocol.MethodWorkspaceAccessGrant {
			err = S.ctrl.Call(method, protocol.WorkspaceAccessGrantParams{WorkspaceID: ws, MemberIDs: []string{m.id}}, nil)
		} else {
			err = S.ctrl.Call(method, protocol.WorkspaceAccessRevokeParams{WorkspaceID: ws, MemberID: m.id}, nil)
		}
		if err != nil {
			t.Fatalf("S %s %s %s: %v", method, ws, m.name, err)
		}
	}
	// An owner shares with a member they see, who may later lose the
	// workspace that made them visible; the share stays.
	share := func(owner, with *wsaMember, via string) {
		t.Helper()
		if via != "" {
			access(protocol.MethodWorkspaceAccessGrant, via, with)
			defer access(protocol.MethodWorkspaceAccessRevoke, via, with)
		}
		if o := shareCall(owner, with.id); o.refused() {
			t.Fatalf("%s account.share with %s: %+v", owner.name, with.name, o)
		}
		t.Cleanup(func() {
			_ = owner.ctrl.Call(protocol.MethodAccountRevoke, protocol.AccountMemberParams{MemberID: with.id}, nil)
		})
	}
	share(B, U, w.wsB)
	share(A, U, "")
	share(A, N, w.wsA)
	launch := func(m *wsaMember, ws, account string) outcome {
		return call(m.ctrl, protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: ws, Task: "access-share", Harness: "fake",
			Mode: string(domain.LaunchTUI), AccountMemberID: account})
	}
	if o := launch(U, w.wsA, B.id); !o.refused() {
		t.Errorf("U launched in wsA on B's account, whose owner does not reach wsA: %s", o.Result)
	} else if strings.Contains(o.Message, w.wsB) {
		t.Errorf("the refusal names B's workspace: %+v", o)
	}
	if o := launch(U, w.wsA, A.id); o.refused() {
		t.Errorf("U launch in wsA on A's account, who owns wsA: %+v", o)
	}
	f = launch(N, w.wsA, A.id)
	if !f.refused() {
		t.Errorf("N launched in unreachable wsA on A's account: %s", f.Result)
	}
	assertLikeMissing(t, "N launch on A's account", f, launch(N, missingID(w.wsA), A.id), w.wsA, missingID(w.wsA))
}

// assertChildren is a sub-session of a run whose owner lost its
// workspace: the run's socket refuses child calls once the grant is gone.
func (w *wsaEnv) assertChildren(ctx context.Context, t *testing.T) {
	socket := waitCoordSocket(t, w.e.coordDir(w.runU))
	if o := coordCall(ctx, socket, protocol.MethodChildOptions, protocol.ChildOptionsParams{}); o.refused() {
		t.Fatalf("child.options from U's run in wsA: %+v", o)
	}
	st := w.srv.srv.Store()
	U, A := w.members["U"], w.members["A"]
	if err := st.RevokeWorkspaceGrant(ctx, domain.WorkspaceID(w.wsA), domain.MemberID(U.id), true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.GrantWorkspace(context.Background(), domain.WorkspaceID(w.wsA), domain.MemberID(U.id), domain.MemberID(A.id), true)
	})
	for _, method := range []string{protocol.MethodChildOptions, protocol.MethodChildStart} {
		params := any(protocol.ChildOptionsParams{})
		if method == protocol.MethodChildStart {
			params = protocol.ChildStartParams{Harness: "fake", Purpose: "access probe", Task: "probe", IdempotencyKey: "access-child"}
		}
		if o := coordCall(ctx, socket, method, params); !o.refused() {
			t.Errorf("%s from a run whose owner lost wsA succeeded: %s", method, o.Result)
		} else if o.Code == protocol.CodeDenied && !strings.Contains(o.Message, "no longer has access") {
			t.Errorf("%s refusal = %+v, want it to say the owner lost the workspace", method, o)
		}
	}
}

func (w *wsaEnv) assertCLI(t *testing.T) {
	aether := buildCLIBinary(t)
	user := map[string]*sshUser{}
	for _, name := range []string{"S", "A", "N"} {
		user[name] = newSSHUser(t, aether, w.srv.addr, w.members[name].keyPath)
	}
	run := func(name string, args ...string) (string, string, int) {
		t.Helper()
		return user[name].run("", aether, args...)
	}
	for name, want := range map[string][]string{"S": {"access-a", "access-b"}, "A": {"access-a"}, "N": nil} {
		out, stderr, code := run(name, "workspace", "list")
		if code != 0 {
			t.Errorf("%s aether workspace list exited %d: %s", name, code, stderr)
			continue
		}
		for _, ws := range []string{"access-a", "access-b"} {
			if strings.Contains(out, ws) != slices.Contains(want, ws) {
				t.Errorf("%s aether workspace list shows %s = %v, want %v:\n%s", name, ws, strings.Contains(out, ws), slices.Contains(want, ws), out)
			}
		}
		if len(want) > 0 && (!strings.Contains(out, "OWNER") || !strings.Contains(out, "ROLE")) {
			t.Errorf("%s aether workspace list has no OWNER and ROLE columns:\n%s", name, out)
		}
	}

	U, B, V, N := w.members["U"], w.members["B"], w.members["V"], w.members["N"]
	missing := missingID(w.wsB)
	_, foreign, fc := run("A", "workspace", "grant", w.wsB, U.id)
	_, absent, mc := run("A", "workspace", "grant", missing, U.id)
	if fc == 0 || fc != mc || normalize(foreign, w.wsB, missing) != normalize(absent, w.wsB, missing) {
		t.Errorf("A aether workspace grant into wsB = %d %q, into a missing workspace = %d %q", fc, foreign, mc, absent)
	}
	_, foreign, fc = run("A", "workspace", "grant", w.wsA, B.id)
	_, absent, mc = run("A", "workspace", "grant", w.wsA, missingID(B.id))
	if fc == 0 || fc != mc || normalize(foreign, B.id, missingID(B.id)) != normalize(absent, B.id, missingID(B.id)) {
		t.Errorf("A aether workspace grant of B, whom A cannot see, = %d %q; of a missing member = %d %q", fc, foreign, mc, absent)
	}
	if _, stderr, code := run("A", "member", "role", U.id, "admin"); code == 0 || !strings.Contains(stderr, fmt.Sprintf("rpc error %d", protocol.CodeDenied)) {
		t.Errorf("A aether member role U admin = %d %q, want the server's denial", code, stderr)
	}
	if out, stderr, code := run("S", "workspace", "grant", w.wsB, N.id); code != 0 || !strings.Contains(out, "granted "+N.id) {
		t.Errorf("S aether workspace grant N into wsB = %d %q %q", code, out, stderr)
	}
	if out, stderr, code := run("N", "workspace", "list"); code != 0 || !strings.Contains(out, "access-b") || strings.Contains(out, "access-a") {
		t.Errorf("N aether workspace list after the grant = %d %q %q, want only access-b", code, out, stderr)
	}
	if _, stderr, code := run("S", "workspace", "revoke", w.wsB, N.id); code != 0 {
		t.Errorf("S aether workspace revoke N from wsB = %d %q", code, stderr)
	}
	if out, stderr, code := run("S", "member", "role", V.id, "viewer"); code != 0 || !strings.Contains(out, "to viewer") {
		t.Errorf("S aether member role V viewer = %d %q %q", code, out, stderr)
	}
	if _, stderr, code := run("S", "member", "role", w.members["S"].id, "admin"); code == 0 || !strings.Contains(stderr, "last") {
		t.Errorf("S aether member role of themself to admin = %d %q, want the last super admin refusal", code, stderr)
	}
}

// assertSelfDemotion is the second half of threat case 15: a super admin
// who demotes themself beside another works, and the other still
// administers everything.
func (w *wsaEnv) assertSelfDemotion(t *testing.T) {
	S, B := w.members["S"], w.members["B"]
	role := func(by, m *wsaMember, r domain.Role) outcome {
		return call(by.ctrl, protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: m.id, Role: string(r)})
	}
	if o := role(S, B, domain.RoleSuperAdmin); o.refused() {
		t.Fatalf("S promotes B to super admin: %+v", o)
	}
	if o := role(S, S, domain.RoleAdmin); o.refused() {
		t.Fatalf("S demotes themself beside super admin B: %+v", o)
	}
	if got := memberInfo(t, S.ctrl).Role; got != string(domain.RoleAdmin) {
		t.Errorf("S is %q after demoting themself, want admin", got)
	}
	if o := call(S.ctrl, protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: w.wsA}); !o.refused() {
		t.Errorf("S, now an admin owning nothing, still reads wsA: %s", o.Result)
	}
	if o := call(B.ctrl, protocol.MethodWorkspaceAccessGrant, protocol.WorkspaceAccessGrantParams{WorkspaceID: w.wsA, MemberIDs: []string{w.members["N"].id}}); o.refused() {
		t.Errorf("super admin B grants N into A's wsA: %+v", o)
	}
	if o := call(B.ctrl, protocol.MethodWorkspaceAccessRevoke, protocol.WorkspaceAccessRevokeParams{WorkspaceID: w.wsA, MemberID: w.members["N"].id}); o.refused() {
		t.Errorf("super admin B revokes N from wsA: %+v", o)
	}
	if o := role(B, S, domain.RoleSuperAdmin); o.refused() {
		t.Fatalf("B restores S: %+v", o)
	}
	if o := role(S, B, domain.RoleAdmin); o.refused() {
		t.Fatalf("S demotes B back: %+v", o)
	}
	if o := role(B, w.members["U"], domain.RoleViewer); o.Code != protocol.CodeDenied {
		t.Errorf("B, an admin again, changes U's role = %+v, want denied", o)
	}
}

// assertMultiRepo is the existence rule for a session with an extra
// repository: it exists only for a member who reaches every one of its
// workspaces, while it lives and after it is deleted.
func (w *wsaEnv) assertMultiRepo(t *testing.T) {
	S := w.members["S"]
	var launched protocol.RunLaunchResult
	if err := S.ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: w.wsA, ExtraWorkspaceIDs: []string{w.wsB}, Task: "two-repos", Harness: "fake", Mode: string(domain.LaunchTUI),
	}, &launched); err != nil {
		t.Fatalf("S run.launch in wsA with wsB: %v", err)
	}
	id := launched.Run.ID
	if len(launched.Run.Repos) != 2 {
		t.Fatalf("S's launch result repos = %+v, want wsA's and wsB's", launched.Run.Repos)
	}
	for _, name := range wsaNames {
		m := w.members[name]
		var list protocol.RunListResult
		if err := m.ctrl.Call(protocol.MethodRunList, protocol.RunListParams{}, &list); err != nil {
			t.Fatalf("%s run.list: %v", name, err)
		}
		listed := slices.ContainsFunc(list.Runs, func(r protocol.Run) bool { return r.ID == id })
		if listed != (name == "S") {
			t.Errorf("%s run.list lists the two-repo session = %v, want %v", name, listed, name == "S")
		}
		if name == "S" {
			continue
		}
		for method, params := range map[string]func(string) any{
			protocol.MethodRunGet:      func(id string) any { return protocol.RunIDParams{RunID: id} },
			protocol.MethodRunPull:     func(id string) any { return protocol.RunPullParams{RunID: id, Repo: w.wsB} },
			protocol.MethodChangesList: func(id string) any { return protocol.ChangesListParams{RunID: id} },
		} {
			assertLikeMissing(t, name+" "+method+" of the two-repo session", call(m.ctrl, method, params(id)), call(m.ctrl, method, params(missingID(id))), id, missingID(id))
		}
	}
	if err := S.ctrl.Call(protocol.MethodRunDelete, protocol.RunIDParams{RunID: id}, nil); err != nil {
		t.Fatalf("S run.delete: %v", err)
	}
	for _, name := range []string{"S", "A", "U"} {
		deleted := false
		for cursor := "0"; ; {
			var page protocol.ChangesListResult
			if err := w.members[name].ctrl.Call(protocol.MethodChangesList, protocol.ChangesListParams{
				Cursor: cursor, WorkspaceID: w.wsA, Kinds: []string{protocol.ChangeKindLifecycle},
			}, &page); err != nil {
				t.Fatalf("%s changes.list: %v", name, err)
			}
			deleted = deleted || slices.ContainsFunc(page.Changes, func(c protocol.Change) bool { return c.RunID == id })
			if !page.More {
				break
			}
			cursor = page.NextCursor
		}
		if deleted != (name == "S") {
			t.Errorf("%s changes.list shows the two-repo session's deletion = %v, want %v", name, deleted, name == "S")
		}
	}
}
