package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func controlClient(t *testing.T, e *testEnv) *protocol.Client {
	t.Helper()
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	return protocol.NewClient(pipe)
}

func TestAuthKnownKeyAndServerInfo(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatalf("server.info: %v", err)
	}
	if info.ProtocolVersion != protocol.Version {
		t.Errorf("protocol_version = %q, want %q", info.ProtocolVersion, protocol.Version)
	}
	if info.Member.ID != string(e.member.ID) {
		t.Errorf("member.id = %q, want %q (the caller)", info.Member.ID, e.member.ID)
	}
	if _, err := time.Parse(time.RFC3339, info.Time); err != nil {
		t.Errorf("time %q is not RFC3339: %v", info.Time, err)
	}
}

func TestAuthUnknownKeyRejected(t *testing.T) {
	e := newTestEnv(t, nil)
	var banner strings.Builder
	client, err := e.dialWith(newSigner(t), &banner)
	if err == nil {
		_ = client.Close()
		t.Fatal("expected auth failure for unknown key")
	}
	if !strings.Contains(banner.String(), "no Aether member for this key") {
		t.Errorf("banner = %q, want it to name the missing member", banner.String())
	}
}

func TestControlListsAndGets(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	var wl protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, nil, &wl); err != nil {
		t.Fatalf("workspace.list: %v", err)
	}
	if len(wl.Workspaces) != 1 || wl.Workspaces[0].ID != string(e.ws.ID) {
		t.Errorf("workspace.list = %+v, want the one workspace", wl.Workspaces)
	}

	var wg protocol.WorkspaceGetResult
	if err := c.Call(protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: string(e.ws.ID)}, &wg); err != nil {
		t.Fatalf("workspace.get: %v", err)
	}
	if wg.Workspace.ID != string(e.ws.ID) || wg.Workspace.BaseBranch != "main" {
		t.Errorf("workspace.get = %+v, want the one workspace on main", wg.Workspace)
	}

	var ml protocol.MemberListResult
	if err := c.Call(protocol.MethodMemberList, nil, &ml); err != nil {
		t.Fatalf("member.list: %v", err)
	}
	if len(ml.Members) != 1 || ml.Members[0].DisplayName != "Ada" {
		t.Errorf("member.list = %+v, want Ada", ml.Members)
	}

	// run.get carries the stored reason and the scheduler-decorated
	// paused flag.
	if err := e.store.UpdateRunStatus(context.Background(), e.run.ID, domain.RunNeedsAttention, "stalled: no output", nil, nil); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	e.runs.setPaused(e.run.ID, true)
	var rg protocol.RunResult
	if err := c.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: string(e.run.ID)}, &rg); err != nil {
		t.Fatalf("run.get: %v", err)
	}
	if rg.Run.Status != "needs-attention" || rg.Run.Branch != e.run.Branch {
		t.Errorf("run.get = %+v", rg.Run)
	}
	if rg.Run.Reason != "stalled: no output" {
		t.Errorf("run.get reason = %q, want %q", rg.Run.Reason, "stalled: no output")
	}
	if !rg.Run.Paused {
		t.Error("run.get did not carry paused")
	}
	if rg.Run.StartedAt != nil {
		t.Errorf("started_at = %v, want null", *rg.Run.StartedAt)
	}

	var rl protocol.RunListResult
	if err := c.Call(protocol.MethodRunList, protocol.RunListParams{}, &rl); err != nil {
		t.Fatalf("run.list: %v", err)
	}
	if len(rl.Runs) != 1 {
		t.Errorf("run.list = %+v, want one run", rl.Runs)
	}
	if err := c.Call(protocol.MethodRunList, protocol.RunListParams{MemberID: "m_nobody"}, &rl); err != nil {
		t.Fatalf("run.list filtered: %v", err)
	}
	if len(rl.Runs) != 0 {
		t.Errorf("run.list for unknown member = %+v, want empty", rl.Runs)
	}
}

func TestControlRunLifecycleMethods(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	var lr protocol.RunResult
	err := c.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "build it", Harness: "claude",
	}, &lr)
	if err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	if lr.Run.Mode != "tui" {
		t.Errorf("launch mode defaulted to %q, want tui", lr.Run.Mode)
	}

	for _, m := range []string{protocol.MethodRunKill, protocol.MethodRunPause, protocol.MethodRunResume} {
		if err := c.Call(m, protocol.RunIDParams{RunID: string(e.run.ID)}, nil); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}
	if err := c.Call(protocol.MethodRunInject, protocol.RunInjectParams{RunID: string(e.run.ID), Message: "focus"}, nil); err != nil {
		t.Fatalf("run.inject: %v", err)
	}
	var cr protocol.RunResult
	if err := c.Call(protocol.MethodRunClose, protocol.RunCloseParams{RunID: string(e.run.ID), Outcome: "merged"}, &cr); err != nil {
		t.Fatalf("run.close: %v", err)
	}
	var rr protocol.RunResult
	if err := c.Call(protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: string(e.run.ID)}, &rr); err != nil {
		t.Fatalf("run.relaunch: %v", err)
	}
	if rr.Run.ID != "run_relaunched" {
		t.Errorf("relaunch returned %q, want the new run", rr.Run.ID)
	}

	want := []string{
		"launch:" + string(e.ws.ID) + ":" + string(e.member.ID) + ":build it:claude:tui",
		"kill:" + string(e.run.ID) + ":" + string(e.member.ID),
		"pause:" + string(e.run.ID) + ":" + string(e.member.ID),
		"resume:" + string(e.run.ID) + ":" + string(e.member.ID),
		"inject:" + string(e.run.ID) + ":" + string(e.member.ID) + ":focus",
		"close:" + string(e.run.ID) + ":" + string(e.member.ID) + ":merged",
		"relaunch:" + string(e.run.ID) + ":" + string(e.member.ID),
	}
	got := e.runs.Calls()
	if len(got) != len(want) {
		t.Fatalf("controller calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A taskless launch is accepted in the default tui mode (the member lands in
// the agent's interactive TUI) and refused in headless mode, which has no
// interactive surface to type into.
func TestControlLaunchTaskOptionalOnlyInTUI(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	var lr protocol.RunResult
	if err := c.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Harness: "claude",
	}, &lr); err != nil {
		t.Fatalf("taskless tui launch: %v", err)
	}
	if lr.Run.Mode != "tui" {
		t.Errorf("taskless launch mode = %q, want tui", lr.Run.Mode)
	}

	var pe *protocol.Error
	err := c.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Harness: "claude", Mode: "headless",
	}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("taskless headless launch = %v, want CodeInvalidParams", err)
	}
}

func TestControlHandoffAndPull(t *testing.T) {
	e := newTestEnv(t, nil)
	other := &domain.Member{DisplayName: "Grace", PublicKey: string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey())), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.store.CreateMember(context.Background(), other); err != nil {
		t.Fatalf("create member: %v", err)
	}
	sub, err := e.bus.Subscribe(context.Background(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	c := controlClient(t, e)
	if cerr := c.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{RunID: string(e.run.ID), ToMemberID: string(other.ID)}, nil); cerr != nil {
		t.Fatalf("run.handoff: %v", cerr)
	}
	run, err := e.store.GetRun(context.Background(), e.run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.MemberID != other.ID {
		t.Errorf("run member = %q, want %q after handoff", run.MemberID, other.ID)
	}
	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.TimelinePayload)
		if !ok || p.Kind != events.TimelineHandoff || p.Message != string(other.ID) {
			t.Errorf("timeline event = %+v, want handoff to %s", ev.Payload, other.ID)
		}
		if ev.ActorID != e.member.ID || ev.RunID != e.run.ID {
			t.Errorf("event actor/run = %s/%s, want %s/%s", ev.ActorID, ev.RunID, e.member.ID, e.run.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no timeline event for handoff")
	}

	var pull protocol.RunPullResult
	if err := c.Call(protocol.MethodRunPull, protocol.RunIDParams{RunID: string(e.run.ID)}, &pull); err != nil {
		t.Fatalf("run.pull: %v", err)
	}
	if pull.WorkspaceID != string(e.ws.ID) || pull.RepoPath != "/"+string(e.ws.ID)+".git" || pull.Branch != e.run.Branch {
		t.Errorf("run.pull = %+v", pull)
	}
}

func TestControlErrorMapping(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	rpcErrOf := func(t *testing.T, err error) *protocol.Error {
		t.Helper()
		var pe *protocol.Error
		if err == nil {
			t.Fatal("expected an rpc error")
		}
		if !errors.As(err, &pe) {
			t.Fatalf("error %v is not *protocol.Error", err)
		}
		return pe
	}

	err := c.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: "run_missing"}, nil)
	if pe := rpcErrOf(t, err); pe.Code != protocol.CodeNotFound {
		t.Errorf("unknown run code = %d, want %d", pe.Code, protocol.CodeNotFound)
	}

	e.runs.setErr(errInvalidTransition)
	err = c.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: string(e.run.ID)}, nil)
	if pe := rpcErrOf(t, err); pe.Code != protocol.CodeInvalidState {
		t.Errorf("invalid transition code = %d, want %d", pe.Code, protocol.CodeInvalidState)
	}
	e.runs.setErr(nil)

	err = c.Call(protocol.MethodRunClose, protocol.RunCloseParams{RunID: string(e.run.ID), Outcome: "victory"}, nil)
	if pe := rpcErrOf(t, err); pe.Code != protocol.CodeInvalidParams {
		t.Errorf("bad outcome code = %d, want %d", pe.Code, protocol.CodeInvalidParams)
	}

	err = c.Call("run.explode", nil, nil)
	if pe := rpcErrOf(t, err); pe.Code != protocol.CodeMethodNotFound {
		t.Errorf("unknown method code = %d, want %d", pe.Code, protocol.CodeMethodNotFound)
	}
}

func TestControlFramingErrors(t *testing.T) {
	e := newTestEnv(t, nil)
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	r := bufio.NewReader(pipe)

	send := func(line string) protocol.Response {
		t.Helper()
		if _, err := pipe.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		raw, err := protocol.ReadLine(r)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp protocol.Response
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal %q: %v", raw, err)
		}
		return resp
	}

	if resp := send(`{not json`); resp.Error == nil || resp.Error.Code != protocol.CodeParse {
		t.Errorf("bad json response = %+v, want parse error", resp)
	}
	if resp := send(`{"jsonrpc":"2.0","method":"server.info"}`); resp.Error == nil || resp.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("missing id response = %+v, want invalid request", resp)
	}
	if resp := send(`{"jsonrpc":"1.0","id":7,"method":"server.info"}`); resp.Error == nil || resp.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("wrong version response = %+v, want invalid request", resp)
	}
	resp := send(`{"jsonrpc":"2.0","id":42,"method":"server.info","params":{}}`)
	if resp.Error != nil || string(resp.ID) != "42" {
		t.Errorf("good request response = %+v, want result with id 42", resp)
	}
}

// The 32 MiB line cap exists for an approved member's profile.push. A
// member who is merely pending must not be able to make the server buffer
// lines that size before the per-method pending gate ever runs; approval
// must lift the cap on the connection the pending member already holds.
func TestPendingMemberOversizedLineRefused(t *testing.T) {
	e := newTestEnv(t, withProfiles(t))
	pat, pending := addMember(t, e, "Pat", domain.RoleCollaborator, true)
	client, err := e.dialWith(pat, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	pipe := openSubsystem(t, client, protocol.SubsystemControl, nil)
	r := bufio.NewReader(pipe)

	oversized := `{"jsonrpc":"2.0","id":1,"method":"server.info","params":{"pad":"` +
		strings.Repeat("A", 1<<20) + `"}}` + "\n"
	_, werr := pipe.Write([]byte(oversized))
	rerr := readLineWithin(t, r, 5*time.Second)
	if werr == nil && rerr == nil {
		t.Fatal("pending member's oversized line was answered instead of refused")
	}

	// The same member, once approved, keeps the large-line budget the
	// profile push needs.
	if err := e.store.ApproveMember(context.Background(), pending.ID); err != nil {
		t.Fatalf("approve member: %v", err)
	}
	approved := controlAs(t, e, pat)
	var push protocol.ProfilePushResult
	if err := approved.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files: []protocol.ProfileFile{
			{Path: "settings.json", Mode: 0o644, Content: bytes.Repeat([]byte("x"), 256<<10)},
		},
	}, &push); err != nil {
		t.Fatalf("approved profile.push: %v", err)
	}
	if push.Snapshot.ID == "" {
		t.Fatalf("snapshot = %+v, want a stored snapshot", push.Snapshot)
	}
}

// The pending gate must be re-checked per read, not once at channel open,
// so an approval reaches a connection that is already parked on a read.
func TestPendingConnectionUnblockedByApproval(t *testing.T) {
	e := newTestEnv(t, nil)
	pat, pending := addMember(t, e, "Pat", domain.RoleCollaborator, true)
	c := controlAs(t, e, pat)
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, nil); err == nil {
		t.Fatal("pending workspace.list succeeded")
	}
	if err := e.store.ApproveMember(context.Background(), pending.ID); err != nil {
		t.Fatalf("approve member: %v", err)
	}
	var wl protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &wl); err != nil {
		t.Fatalf("approved workspace.list on the pending connection: %v", err)
	}
}

func readLineWithin(t *testing.T, r *bufio.Reader, d time.Duration) error {
	t.Helper()
	ch := make(chan error, 1)
	go func() {
		_, err := protocol.ReadLine(r)
		ch <- err
	}()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		t.Fatal("timed out waiting for a line")
		return nil
	}
}
