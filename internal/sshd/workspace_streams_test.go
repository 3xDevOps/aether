package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// scopedWorld is a scoped server with the seeded super admin's workspace
// and run, and a foreign workspace with its own run.
type scopedWorld struct {
	*testEnv
	foreign    *domain.Workspace
	foreignRun *domain.Run
}

func newScopedWorld(t *testing.T, mod func(*Config)) *scopedWorld {
	t.Helper()
	e := newScopedTestEnv(t, mod)
	w := &scopedWorld{testEnv: e, foreign: &domain.Workspace{Name: "foreign", BaseBranch: "main"}}
	if err := e.db.CreateWorkspace(context.Background(), w.foreign); err != nil {
		t.Fatal(err)
	}
	w.foreignRun = &domain.Run{WorkspaceID: w.foreign.ID, MemberID: e.member.ID, Task: "elsewhere",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning, ACP: true}
	if err := e.db.CreateRun(context.Background(), w.foreignRun); err != nil {
		t.Fatal(err)
	}
	return w
}

// granted adds a member granted the given workspaces.
func (w *scopedWorld) granted(t *testing.T, name string, role domain.Role, workspaces ...domain.WorkspaceID) (ssh.Signer, *domain.Member) {
	t.Helper()
	signer, m := addMember(t, w.testEnv, name, role, false)
	for _, ws := range workspaces {
		if err := w.db.GrantWorkspace(context.Background(), ws, m.ID, w.member.ID); err != nil {
			t.Fatal(err)
		}
	}
	return signer, m
}

func sameError(got, want *protocol.Error) bool {
	return got != nil && want != nil && got.Code == want.Code && got.Message == want.Message
}

func eventLines(t *testing.T, stream io.Reader) <-chan protocol.Event {
	t.Helper()
	out := make(chan protocol.Event, 16)
	go func() {
		defer close(out)
		r := bufio.NewReader(stream)
		for {
			line, err := protocol.ReadLine(r)
			if err != nil {
				return
			}
			var ev protocol.Event
			if json.Unmarshal(line, &ev) == nil {
				out <- ev
			}
		}
	}()
	return out
}

func nextEvent(t *testing.T, lines <-chan protocol.Event) protocol.Event {
	t.Helper()
	select {
	case ev, ok := <-lines:
		if !ok {
			t.Fatal("event stream ended")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
	}
	return protocol.Event{}
}

// The event feed carries only the workspaces its member reaches, follows a
// grant and a revoke made while it streams, and refuses a foreign
// workspace filter exactly as a workspace that does not exist.
func TestEventsCarryOnlyReachableWorkspaces(t *testing.T) {
	t.Parallel()
	w := newScopedWorld(t, func(c *Config) { c.revalidateInterval = time.Hour })
	ctx := context.Background()
	_, u := w.granted(t, "Una", domain.RoleCollaborator, w.ws.ID)
	local := w.srv.Local(u.ID)

	_, missing := local.Events(ctx, protocol.SubscribeRequest{WorkspaceID: "ws-doesnotexist"})
	_, foreign := local.Events(ctx, protocol.SubscribeRequest{WorkspaceID: string(w.foreign.ID)})
	var want, got *protocol.Error
	if !errors.As(missing, &want) || want.Code != protocol.CodeNotFound || !errors.As(foreign, &got) || !sameError(got, want) {
		t.Fatalf("foreign workspace filter = %v, missing workspace = %v; want the same not found", foreign, missing)
	}

	stream, err := local.Events(ctx, protocol.SubscribeRequest{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer func() { _ = stream.Close() }()
	lines := eventLines(t, stream)
	publish := func(ws domain.WorkspaceID, run domain.RunID) {
		t.Helper()
		if _, err := w.bus.Publish(ctx, events.Event{WorkspaceID: ws, RunID: run, ActorID: w.member.ID,
			Payload: events.PresencePayload{State: events.PresenceOnline}}); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(what string, run domain.RunID) {
		t.Helper()
		if ev := nextEvent(t, lines); ev.RunID != string(run) {
			t.Fatalf("%s: event of %s (workspace %s), want %s", what, ev.RunID, ev.WorkspaceID, run)
		}
	}

	publish(w.foreign.ID, w.foreignRun.ID)
	publish(w.ws.ID, w.run.ID)
	expect("before the grant", w.run.ID)

	if err := w.db.GrantWorkspace(ctx, w.foreign.ID, u.ID, w.member.ID); err != nil {
		t.Fatal(err)
	}
	w.srv.accessChanged()
	publish(w.foreign.ID, w.foreignRun.ID)
	expect("after the grant", w.foreignRun.ID)

	if err := w.db.RevokeWorkspaceGrant(ctx, w.foreign.ID, u.ID, ""); err != nil {
		t.Fatal(err)
	}
	w.srv.accessChanged()
	publish(w.foreign.ID, w.foreignRun.ID)
	publish(w.ws.ID, w.run.ID)
	expect("after the revoke", w.run.ID)
}

// Read-only attach and the ACP stream of a run in a workspace the member
// cannot reach answer as for a run that does not exist; a live read-only
// attach ends when the grant goes.
func TestAttachAndACPOfAForeignRunAreNotFound(t *testing.T) {
	t.Parallel()
	w := newScopedWorld(t, func(c *Config) { c.revalidateInterval = 20 * time.Millisecond })
	ctx := context.Background()
	signer, u := w.granted(t, "Una", domain.RoleViewer, w.ws.ID)

	missing := attachAs(t, w.testEnv, signer, "run-doesnotexist", false)
	foreign := attachAs(t, w.testEnv, signer, w.foreignRun.ID, false)
	if missing.Code != protocol.CodeNotFound || foreign.OK || foreign.Code != missing.Code || foreign.Error != missing.Error {
		t.Fatalf("attach to a foreign run = %+v, to a missing run = %+v; want the same not found", foreign, missing)
	}
	_, missingACP, _ := w.srv.Local(u.ID).ACP(ctx, protocol.ACPStreamRequest{RunID: "run-doesnotexist"})
	_, foreignACP, _ := w.srv.Local(u.ID).ACP(ctx, protocol.ACPStreamRequest{RunID: string(w.foreignRun.ID)})
	if missingACP.Code != protocol.CodeNotFound || foreignACP.Code != missingACP.Code || foreignACP.Error != missingACP.Error {
		t.Fatalf("ACP of a foreign run = %+v, of a missing run = %+v; want the same not found", foreignACP, missingACP)
	}

	term, ack, err := w.srv.Local(u.ID).Attach(ctx, protocol.AttachRequest{RunID: string(w.run.ID), ReadOnly: true, Cols: 80, Rows: 24})
	if err != nil || !ack.OK {
		t.Fatalf("read-only attach in a granted workspace: ack=%+v err=%v", ack, err)
	}
	if err := w.db.RevokeWorkspaceGrant(ctx, w.ws.ID, u.ID, ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, rerr := io.ReadAll(term)
		done <- rerr
	}()
	select {
	case rerr := <-done:
		var exit *protocol.RemoteExitError
		if !errors.As(rerr, &exit) || exit.Status != protocol.AttachExitMembershipRevoked {
			t.Fatalf("attach after the revoke ended with %v, want exit status %d", rerr, protocol.AttachExitMembershipRevoked)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read-only attach stayed open after the grant was revoked")
	}
}

func runGit(t *testing.T, e *testEnv, signer ssh.Signer, op string, ws domain.WorkspaceID) (string, error) {
	t.Helper()
	client := e.dialWithTest(t, signer)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close() //nolint:errcheck // test cleanup
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	err = sess.Run(fmt.Sprintf("git-%s '/%s.git'", op, ws))
	return stderr.String(), err
}

// Git over SSH needs the workspace: an unreachable one is refused as a
// missing one, a granted viewer fetches but cannot push.
func TestGitTransportNeedsTheWorkspace(t *testing.T) {
	t.Parallel()
	w := newScopedWorld(t, nil)
	viewer, _ := w.granted(t, "Vera", domain.RoleViewer, w.ws.ID)

	for _, op := range []string{"upload-pack", "receive-pack"} {
		missing, _ := runGit(t, w.testEnv, viewer, op, "ws-doesnotexist")
		foreign, err := runGit(t, w.testEnv, viewer, op, w.foreign.ID)
		if err == nil || strings.ReplaceAll(foreign, string(w.foreign.ID), "ws-doesnotexist") != missing ||
			missing != fmt.Sprintf("aether: workspace %q: %v\n", "ws-doesnotexist", store.ErrNotFound) {
			t.Fatalf("%s of a foreign workspace = %q (%v), of a missing one = %q; want the same not found", op, foreign, err, missing)
		}
	}
	if stderr, err := runGit(t, w.testEnv, viewer, "upload-pack", w.ws.ID); err != nil {
		t.Fatalf("viewer fetch of a granted workspace: %v (%s)", err, stderr)
	}
	if stderr, err := runGit(t, w.testEnv, viewer, "receive-pack", w.ws.ID); err == nil || !strings.Contains(stderr, "permission denied") {
		t.Fatalf("viewer push = %q (%v), want permission denied", stderr, err)
	}
	if calls := w.git.Calls(); len(calls) != 1 || calls[0] != "upload-pack:"+string(w.ws.ID) {
		t.Fatalf("git transport calls = %v, want only the granted fetch", calls)
	}
}

// stalledGit advertises refs and then waits for its context, as a
// receive-pack does while the client holds back its pack.
type stalledGit struct {
	fakeGit
	started chan struct{}
}

func (g *stalledGit) ReceivePack(ctx context.Context, _ domain.WorkspaceID, _ io.Reader, stdout, _ io.Writer) (int, error) {
	if _, err := io.WriteString(stdout, pktLine(testAdvertisedSHA+" refs/heads/main\x00report-status\n")+"0000"); err != nil {
		return 128, err
	}
	close(g.started)
	<-ctx.Done()
	return 128, ctx.Err()
}

// A push left waiting ends when its member's grant is revoked, without
// waiting for the periodic re-check.
func TestGitPushEndsWhenAccessIsRevoked(t *testing.T) {
	t.Parallel()
	git := &stalledGit{started: make(chan struct{})}
	w := newScopedWorld(t, func(c *Config) {
		c.Git = git
		c.revalidateInterval = time.Hour
	})
	signer, u := w.granted(t, "Una", domain.RoleCollaborator, w.foreign.ID)
	pushed := make(chan string, 1)
	go func() {
		stderr, _ := runGit(t, w.testEnv, signer, "receive-pack", w.foreign.ID)
		pushed <- stderr
	}()
	select {
	case <-git.started:
	case <-time.After(5 * time.Second):
		t.Fatal("receive-pack did not start")
	}
	raw, err := json.Marshal(protocol.WorkspaceAccessRevokeParams{WorkspaceID: string(w.foreign.ID), MemberID: string(u.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := w.srv.workspaceAccessRevoke(t.Context(), w.member.ID, raw); perr != nil {
		t.Fatalf("revoke: %+v", perr)
	}
	select {
	case stderr := <-pushed:
		if !strings.Contains(stderr, "access ended") || !strings.Contains(stderr, store.ErrNotFound.Error()) {
			t.Fatalf("push stderr = %q, want access ended, not found", stderr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receive-pack still runs after its member's grant was revoked")
	}
}

// A launch needs every workspace it names, and a shared account's owner
// must reach them too.
func TestLaunchNeedsEveryWorkspace(t *testing.T) {
	t.Parallel()
	w := newScopedWorld(t, nil)
	ctx := context.Background()
	_, u := w.granted(t, "Una", domain.RoleCollaborator, w.ws.ID)
	_, owner := w.granted(t, "Otto", domain.RoleCollaborator, w.foreign.ID)
	if _, err := w.db.ShareAccount(ctx, owner.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	launch := func(p protocol.RunLaunchParams) *protocol.Error {
		t.Helper()
		p.Harness = "claude"
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		_, perr := w.srv.dispatch(ctx, u.ID, protocol.MethodRunLaunch, raw)
		return perr
	}

	missing := launch(protocol.RunLaunchParams{WorkspaceID: "ws-doesnotexist"})
	if missing == nil || missing.Code != protocol.CodeNotFound {
		t.Fatalf("launch in a missing workspace = %v, want not found", missing)
	}
	if perr := launch(protocol.RunLaunchParams{WorkspaceID: string(w.foreign.ID)}); !sameError(perr, missing) {
		t.Fatalf("launch in a foreign workspace = %v, want %v", perr, missing)
	}
	if perr := launch(protocol.RunLaunchParams{WorkspaceID: string(w.ws.ID), ExtraWorkspaceIDs: []string{string(w.foreign.ID)}}); !sameError(perr, missing) {
		t.Fatalf("launch with a foreign extra workspace = %v, want %v", perr, missing)
	}
	perr := launch(protocol.RunLaunchParams{WorkspaceID: string(w.ws.ID), AccountMemberID: string(owner.ID)})
	if perr == nil || perr.Code != protocol.CodeDenied || !strings.Contains(perr.Message, "has no access to workspace "+string(w.ws.ID)) {
		t.Fatalf("launch on an account whose owner lacks the workspace = %v, want denied", perr)
	}
	if calls := w.runs.Calls(); len(calls) != 0 {
		t.Fatalf("scheduler calls after refused launches = %v, want none", calls)
	}
	if err := w.db.GrantWorkspace(ctx, w.ws.ID, owner.ID, w.member.ID); err != nil {
		t.Fatal(err)
	}
	if perr := launch(protocol.RunLaunchParams{WorkspaceID: string(w.ws.ID), AccountMemberID: string(owner.ID)}); perr != nil {
		t.Fatalf("launch on the shared account once its owner reaches the workspace: %v", perr)
	}
}

// A run is handed off only to a member who can launch in each of its
// workspaces.
func TestHandoffNeedsARecipientWithTheWorkspace(t *testing.T) {
	t.Parallel()
	w := newScopedWorld(t, nil)
	ctx := context.Background()
	_, n := w.granted(t, "Nia", domain.RoleCollaborator, w.foreign.ID)
	handoff := func() error {
		return handlerCallJSON(t, w.testEnv, w.member.ID, protocol.MethodRunHandoff, protocol.RunHandoffParams{
			RunID: string(w.run.ID), ToMemberID: string(n.ID),
		}, nil)
	}
	var pe *protocol.Error
	if err := handoff(); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams || !strings.Contains(pe.Message, "no access") {
		t.Fatalf("handoff to a member without the workspace = %v, want refused", err)
	}
	if err := w.db.GrantWorkspace(ctx, w.ws.ID, n.ID, w.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := handoff(); err != nil {
		t.Fatalf("handoff once the recipient reaches the workspace: %v", err)
	}
}

// A retained capture of a run in a workspace the member cannot reach is
// refused as one that does not exist.
func TestRetainedArtifactOfAForeignRunIsNotFound(t *testing.T) {
	t.Parallel()
	w := newScopedWorld(t, nil)
	_, u := w.granted(t, "Una", domain.RoleViewer, w.ws.ID)
	packet := handlerPacket("foreign-capture", w.foreign.ID, w.foreignRun.ID, w.member.ID)
	packet.Captures = []protocol.DevArtifact{{ID: "capture", RunID: string(w.foreignRun.ID), ContentType: "image/png", Bytes: 3}}
	if err := w.db.CreateEvidencePacket(t.Context(), packet); err != nil {
		t.Fatal(err)
	}
	w.srv.cfg.Services.Evidence = &retainedArtifactTestService{}
	open := func(run domain.RunID, packetID string) *protocol.Error {
		t.Helper()
		_, _, _, err := w.srv.openDevelopmentArtifact(t.Context(), u.ID, protocol.DevArtifactDownloadRequest{
			DevArtifactGetParams: protocol.DevArtifactGetParams{DevRunParams: protocol.DevRunParams{RunID: string(run)}, ArtifactID: "capture"},
			EvidencePacketID:     packetID,
		})
		if err == nil {
			t.Fatalf("open retained capture %s: admitted", packetID)
		}
		return rpcError(err)
	}
	missing, foreign := open(w.foreignRun.ID, "evp-doesnotexist"), open(w.foreignRun.ID, packet.ID)
	if missing.Code != protocol.CodeNotFound || !sameError(foreign, missing) {
		t.Fatalf("foreign retained capture = %v, missing one = %v; want the same not found", foreign, missing)
	}
	if got := open(w.run.ID, packet.ID); !sameError(got, missing) {
		t.Fatalf("foreign packet under a reachable run = %v, missing one = %v; want the same not found", got, missing)
	}
}

func TestRunWithAnUnreachableExtraRepoIsNotFound(t *testing.T) {
	t.Parallel()
	w := newScopedWorld(t, nil)
	ctx := context.Background()
	signer, u := w.granted(t, "Una", domain.RoleCollaborator, w.ws.ID)
	extra := []domain.RunRepo{{WorkspaceID: w.foreign.ID, Name: "foreign", Path: "/repos/foreign", Branch: "aether/foreign", BaseBranch: "main"}}
	if err := w.db.SetRunRepos(ctx, w.run.ID, extra); err != nil {
		t.Fatal(err)
	}

	missing, got := attachAs(t, w.testEnv, signer, "run-doesnotexist", false), attachAs(t, w.testEnv, signer, w.run.ID, false)
	if missing.Code != protocol.CodeNotFound || got.OK || got.Code != missing.Code || got.Error != missing.Error {
		t.Fatalf("attach to a run with an unreachable extra repo = %+v, to a missing run = %+v; want the same not found", got, missing)
	}
	_, missingACP, _ := w.srv.Local(u.ID).ACP(ctx, protocol.ACPStreamRequest{RunID: "run-doesnotexist"})
	_, gotACP, _ := w.srv.Local(u.ID).ACP(ctx, protocol.ACPStreamRequest{RunID: string(w.run.ID)})
	if gotACP.Code != missingACP.Code || gotACP.Error != missingACP.Error {
		t.Fatalf("ACP = %+v, missing = %+v; want the same not found", gotACP, missingACP)
	}
	missingSync, gotSync := syncAck(t, w.testEnv, signer, "run-doesnotexist", true), syncAck(t, w.testEnv, signer, w.run.ID, true)
	if missingSync.Code != protocol.CodeNotFound || gotSync.OK || gotSync.Code != missingSync.Code || gotSync.Error != missingSync.Error {
		t.Fatalf("sync = %+v, missing = %+v; want the same not found", gotSync, missingSync)
	}
	client := controlAs(t, w.testEnv, signer)
	for _, method := range []string{protocol.MethodRunInject, protocol.MethodRunACPHistory, protocol.MethodRunKill} {
		gotErr := client.Call(method, protocol.RunIDParams{RunID: string(w.run.ID)}, nil)
		missingErr := client.Call(method, protocol.RunIDParams{RunID: "run-doesnotexist"}, nil)
		if gotErr == nil || missingErr == nil || !sameError(wireErrOf(t, gotErr), wireErrOf(t, missingErr)) {
			t.Fatalf("%s = %v, missing = %v; want the same not found", method, gotErr, missingErr)
		}
	}
	if _, _, _, err := w.srv.developmentAuthority(ctx, u.ID, string(w.run.ID)); !errors.Is(err, permissions.ErrNoAccess) {
		t.Fatalf("development authority = %v, want ErrNoAccess", err)
	}

	if err := w.db.GrantWorkspace(ctx, w.foreign.ID, u.ID, w.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, ack, err := w.srv.Local(u.ID).Attach(ctx, protocol.AttachRequest{RunID: string(w.run.ID), ReadOnly: true, Cols: 80, Rows: 24}); err != nil || !ack.OK {
		t.Fatalf("read-only attach once every repo is reachable: ack=%+v err=%v", ack, err)
	}
	if err := checkSteer(ctx, w.store, u.ID, w.run.ID); err != nil {
		t.Fatalf("steer once every repo is reachable: %v", err)
	}
}
