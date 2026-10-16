package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

// mergeEnv is a server under account access whose seeded run is
// finished, with a tailnet resolver that answers only while tailnetDial
// dials.
func mergeEnv(t *testing.T, mod func(*Config)) (*testEnv, *fakeWhoIs) {
	t.Helper()
	whois := &fakeWhoIs{err: errors.New("not on the tailnet")}
	e := newTestEnv(t, func(c *Config) {
		c.WhoIs, c.EdgeAccess = whois, edgeproto.PolicyAccount
		if mod != nil {
			mod(c)
		}
	})
	if _, err := e.store.UpdateRunStatus(context.Background(), e.run.ID, domain.RunCompleted, "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	return e, whois
}

// tailnetDial connects as the tailnet login, a member the store already
// holds.
func tailnetDial(t *testing.T, e *testEnv, whois *fakeWhoIs, login string) *protocol.Client {
	t.Helper()
	whois.set(WhoIsIdentity{Login: login, NodeID: "node-" + login}, nil)
	defer whois.set(WhoIsIdentity{}, errors.New("not on the tailnet"))
	client, err := e.dialAs("tailnet", nil)
	if err != nil {
		t.Fatalf("tailnet dial as %s: %v", login, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return controlClientOn(t, client)
}

func addTailnetMember(t *testing.T, e *testEnv, login string, role domain.Role) *domain.Member {
	t.Helper()
	m := &domain.Member{DisplayName: login, TailnetLogin: login, Color: "#4363d8", Role: role}
	if err := e.store.CreateMember(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func mergeStart(t *testing.T, c *protocol.Client) string {
	t.Helper()
	var res protocol.MemberMergeStartResult
	if err := c.Call(protocol.MethodMemberMergeStart, struct{}{}, &res); err != nil {
		t.Fatalf("member.merge.start: %v", err)
	}
	return res.Code
}

func mergePreview(t *testing.T, c *protocol.Client, code string) protocol.MemberMergePlan {
	t.Helper()
	var plan protocol.MemberMergePlan
	if err := c.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: code}, &plan); err != nil {
		t.Fatalf("member.merge.preview: %v", err)
	}
	return plan
}

func mergePropose(c *protocol.Client, code, keep, digest string, keepAdmin bool) (protocol.MemberMergeProposal, error) {
	var res protocol.MemberMergeProposal
	err := c.Call(protocol.MethodMemberMergePropose, protocol.MemberMergeProposeParams{Code: code, KeepSettingsOf: keep,
		PlanDigest: digest, DiscardAck: true, KeepAdmin: keepAdmin}, &res)
	return res, err
}

func mustPropose(t *testing.T, c *protocol.Client, code, keep string) protocol.MemberMergeProposal {
	t.Helper()
	p, err := mergePropose(c, code, keep, mergePreview(t, c, code).PlanDigest, false)
	if err != nil {
		t.Fatalf("member.merge.propose: %v", err)
	}
	return p
}

func mergeAccept(c *protocol.Client, p protocol.MemberMergeProposal, keepAdmin bool) (protocol.MemberMergeAcceptResult, error) {
	var res protocol.MemberMergeAcceptResult
	err := c.Call(protocol.MethodMemberMergeAccept, protocol.MemberMergeAcceptParams{ProposalID: p.ID,
		PlanDigest: p.PlanDigest, KeepAdmin: keepAdmin}, &res)
	return res, err
}

func wantRefusal(t *testing.T, err error, contains string) {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, contains) {
		t.Fatalf("got %v, want a refusal containing %q", err, contains)
	}
}

// A person who reached the server through Tailscale and through the edge
// as two members merges them: the code minted on the tailnet sign-in, the
// merge proposed from the GitHub one and accepted on the tailnet one. The
// GitHub device then resolves to the survivor, the absorbed member's
// connection is closed, its home is archived, and a replay returns the
// recorded merge.
func TestMergeTailnetMemberWithEdgeMember(t *testing.T) {
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), filepath.Join(t.TempDir(), "caches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	merged := filepath.Join(t.TempDir(), "merged")
	e, whois := mergeEnv(t, func(c *Config) { c.Homes, c.MergedDir = homes, merged })
	ctx := context.Background()
	tina := addTailnetMember(t, e, "octo@example.com", domain.RoleCollaborator)
	tailnet := tailnetDial(t, e, whois, tina.TailnetLogin)

	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop := newSigner(t)
	edgeConn := e.mustDialEdge(t, octo, laptop, "laptop")
	github := controlClientOn(t, edgeConn)
	gh := serverInfoMember(t, github)
	ghHome, err := homes.Path(domain.MemberID(gh.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(ghHome, "notes.txt"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}

	code := mergeStart(t, tailnet)
	// Signing in alone registered the device; no merge step is taken from it.
	wantRefusal(t, github.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: code}, nil), "needs an approved device")
	dev, err := identities(t, e).GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	if err = identities(t, e).ApproveDevice(ctx, dev.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}

	plan := mergePreview(t, github, code)
	if plan.Minted.ID != string(tina.ID) || plan.Proposer.ID != gh.ID || plan.Role != string(domain.RoleCollaborator) ||
		plan.RoleWithKeepAdmin != "" || len(plan.Choices) != 2 || plan.PlanDigest == "" {
		t.Fatalf("plan = %+v", plan)
	}
	if !slices.Contains(plan.SignIns, "tailnet login octo@example.com") || !slices.Contains(plan.SignIns, "github account octo at the edge") {
		t.Fatalf("sign-ins = %v", plan.SignIns)
	}
	if _, err = mergePropose(github, code, string(tina.ID), "0000", false); err == nil || !strings.Contains(err.Error(), "preview again") {
		t.Fatalf("propose with a stale digest = %v", err)
	}
	proposal, err := mergePropose(github, code, string(tina.ID), plan.PlanDigest, false)
	if err != nil {
		t.Fatalf("member.merge.propose: %v", err)
	}
	_, err = mergeAccept(github, proposal, false)
	wantRefusal(t, err, "only member "+string(tina.ID))
	if err = e.store.PutTerminal(ctx, &domain.Terminal{Member: tina.ID, ContainerID: "c1", Image: "img", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_, err = mergeAccept(tailnet, proposal, false)
	wantRefusal(t, err, "the Environment terminal of member "+string(tina.ID))
	if calls := e.runs.Calls(); slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "terminal-stop:") }) {
		t.Fatalf("a refused accept stopped a terminal: %v", calls)
	}
	if err = e.store.DeleteTerminal(ctx, tina.ID); err != nil {
		t.Fatal(err)
	}

	var pending protocol.MemberMergePendingResult
	if err = tailnet.Call(protocol.MethodMemberMergePending, struct{}{}, &pending); err != nil || len(pending.Proposals) != 1 ||
		pending.Proposals[0].ID != proposal.ID || pending.Proposals[0].Plan == nil || pending.Proposals[0].Refusal != "" {
		t.Fatalf("member.merge.pending = %+v, %v", pending, err)
	}
	res, err := mergeAccept(tailnet, pending.Proposals[0], false)
	if err != nil {
		t.Fatalf("member.merge.accept: %v", err)
	}
	if res.Member.ID != string(tina.ID) || res.Merge.AbsorbedID != gh.ID || res.Merge.Discarded.DisplayName != "Octo Cat" ||
		res.Merge.ConfirmedByKind != "ssh" || res.Merge.ArchivePath != filepath.Join(merged, res.Merge.ID) {
		t.Fatalf("merge = %+v", res)
	}
	if data, gerr := os.ReadFile(filepath.Join(res.Merge.ArchivePath, "home", "notes.txt")); gerr != nil || string(data) != "kept" {
		t.Fatalf("archived home: %q, %v", data, gerr)
	}
	waitClosed(t, edgeConn)

	if m := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))); m.ID != string(tina.ID) {
		t.Fatalf("the GitHub device reconnects as %s, want the survivor %s", m.ID, tina.ID)
	}
	if m := serverInfoMember(t, tailnetDial(t, e, whois, tina.TailnetLogin)); m.ID != string(tina.ID) {
		t.Fatalf("the tailnet login reconnects as %s", m.ID)
	}
	var members protocol.MemberListResult
	if err = tailnet.Call(protocol.MethodMemberList, struct{}{}, &members); err != nil || members.MergedInto[gh.ID] != string(tina.ID) {
		t.Fatalf("member.list = %+v, %v; want the absorbed member mapped to the survivor", members, err)
	}
	for _, call := range []struct {
		method string
		params any
	}{
		{protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: gh.ID, Role: string(domain.RoleViewer)}},
		{protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: gh.ID}},
	} {
		wantRefusal(t, controlClient(t, e).Call(call.method, call.params, nil), "was merged into "+string(tina.ID))
	}
	if m, _ := e.store.GetMember(ctx, tina.ID); m.Role != domain.RoleCollaborator {
		t.Fatalf("a call naming the absorbed member changed the survivor: %+v", m)
	}
	if entries, gerr := e.srv.EdgeDirectory(ctx); gerr != nil || len(entries) != 1 || entries[0].Subject != octo.Subject {
		t.Fatalf("edge directory = %+v, %v", entries, gerr)
	}
	replay, err := mergeAccept(tailnet, proposal, false)
	if err != nil || replay.Merge.ID != res.Merge.ID {
		t.Fatalf("replayed accept = %+v, %v", replay, err)
	}
	_, err = mergeAccept(controlClient(t, e), proposal, false)
	wantRefusal(t, err, "accepted already")
	var list protocol.MemberMergeListResult
	if err := tailnet.Call(protocol.MethodMemberMergeList, struct{}{}, &list); err != nil || len(list.Merges) != 1 || list.Merges[0].ID != res.Merge.ID {
		t.Fatalf("member.merge.list = %+v, %v", list, err)
	}
}

func TestMergeRefusals(t *testing.T) {
	t.Parallel()
	e, whois := mergeEnv(t, nil)
	ctx := context.Background()
	admin := controlClient(t, e)
	viewerKey, viewer := addMember(t, e, "Vi", domain.RoleViewer, false)
	vi := controlAs(t, e, viewerKey)

	wantRefusal(t, vi.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: mergeStart(t, vi)}, nil),
		"already the same member")
	wantRefusal(t, vi.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: mergeStart(t, admin)}, nil),
		"both members have an SSH public key")

	pending := addTailnetMember(t, e, "pat@example.com", domain.RoleViewer)
	pending.Pending = true
	if err := e.store.UpdateMember(ctx, pending); err != nil {
		t.Fatal(err)
	}
	pat := tailnetDial(t, e, whois, pending.TailnetLogin)
	wantRefusal(t, pat.Call(protocol.MethodMemberMergeStart, struct{}{}, nil), "pending")

	tina := addTailnetMember(t, e, "tina@example.com", domain.RoleCollaborator)
	tc := tailnetDial(t, e, whois, tina.TailnetLogin)
	run := &domain.Run{WorkspaceID: e.ws.ID, MemberID: viewer.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := e.store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	code := mergeStart(t, vi)
	wantRefusal(t, tc.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: code}, nil), string(run.ID))
	if _, err := e.store.UpdateRunStatus(ctx, run.ID, domain.RunFailed, "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	e.runs.mu.Lock()
	e.runs.retentions = map[domain.RunID]scheduler.RetentionInfo{run.ID: {RetainedUntil: &until}}
	e.runs.mu.Unlock()
	wantRefusal(t, tc.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: code}, nil),
		"finished runs "+string(run.ID)+" still keep their containers")
	e.runs.mu.Lock()
	e.runs.retentions = nil
	e.runs.mu.Unlock()

	plan := mergePreview(t, tc, code)
	if plan.Role != string(domain.RoleViewer) || plan.RoleWithKeepAdmin != "" {
		t.Fatalf("plan role = %s, keep-admin role %q", plan.Role, plan.RoleWithKeepAdmin)
	}
	_, err := mergePropose(tc, code, string(tina.ID), plan.PlanDigest, true)
	wantRefusal(t, err, "keep_admin is the admin's to ask for")
	proposal, err := mergePropose(tc, code, string(tina.ID), plan.PlanDigest, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mergeAccept(vi, proposal, false); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if m, _ := e.store.GetMember(ctx, tina.ID); m.Role != domain.RoleViewer || m.PublicKey != viewer.PublicKey {
		t.Fatalf("merged member = %+v", m)
	}

	hash, _ := mergeCodeHash("AAAA-BBBB-CCCC-DDDD")
	if err = e.store.(store.MergeStore).CreateMergeCode(ctx, e.member.ID, hash, time.Now().Add(-time.Hour), time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, tc.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: "aaaa bbbb cccc dddd"}, nil), "expired")
	wantRefusal(t, tc.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: "AAAA-BBBB-CCCC-EEEE"}, nil), "unknown")

	// A proposal is dropped by either side.
	code = mergeStart(t, admin)
	other := addTailnetMember(t, e, "otto@example.com", domain.RoleCollaborator)
	oc := tailnetDial(t, e, whois, other.TailnetLogin)
	proposal = mustPropose(t, oc, code, string(other.ID))
	var cancelled protocol.MemberMergeCancelResult
	if err = oc.Call(protocol.MethodMemberMergeCancel, protocol.MemberMergeCancelParams{}, &cancelled); err != nil || cancelled.Cancelled != 1 {
		t.Fatalf("member.merge.cancel = %+v, %v", cancelled, err)
	}
	_, err = mergeAccept(admin, proposal, false)
	wantRefusal(t, err, "is not open")
}

// Merging the server's only admin into a collaborator demotes it unless
// the admin side asks to keep it admin, and keep_admin counts only from
// that side. A merge that would leave the owner's GitHub account on a
// member who is not an admin is refused.
func TestMergeLastAdminNeedsKeepAdmin(t *testing.T) {
	t.Parallel()
	e, whois := mergeEnv(t, nil)
	ctx := context.Background()
	tina := addTailnetMember(t, e, "tina@example.com", domain.RoleCollaborator)
	tc := tailnetDial(t, e, whois, tina.TailnetLogin)
	admin := controlClient(t, e)

	code := mergeStart(t, admin)
	plan := mergePreview(t, tc, code)
	if plan.Role != string(domain.RoleCollaborator) || plan.RoleWithKeepAdmin != string(domain.RoleAdmin) || !plan.KeepAdminRequired {
		t.Fatalf("plan = %+v", plan)
	}
	_, err := mergePropose(tc, code, string(e.member.ID), plan.PlanDigest, true)
	wantRefusal(t, err, "keep_admin is the admin's to ask for")
	proposal, err := mergePropose(tc, code, string(e.member.ID), plan.PlanDigest, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mergeAccept(admin, proposal, false)
	wantRefusal(t, err, "last admin")

	_, _ = addMember(t, e, "Zed", domain.RoleAdmin, false)
	if err = identities(t, e).BindIdentity(ctx, &domain.Identity{Member: e.member.ID, Provider: octo.Provider, Subject: octo.Subject, Login: octo.Login}); err != nil {
		t.Fatal(err)
	}
	e.srv.SetEdgeOwner(&fakeEdgeOwner{owner: &octo})
	var pending protocol.MemberMergePendingResult
	if err = admin.Call(protocol.MethodMemberMergePending, struct{}{}, &pending); err != nil || len(pending.Proposals) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if pending.Proposals[0].Refusal == "" {
		t.Fatal("a proposal whose members changed shows no refusal")
	}
	if err = admin.Call(protocol.MethodMemberMergeCancel, protocol.MemberMergeCancelParams{ProposalID: proposal.ID}, nil); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	code = mergeStart(t, admin)
	proposal = mustPropose(t, tc, code, string(e.member.ID))
	_, err = mergeAccept(admin, proposal, false)
	wantRefusal(t, err, "owns this server at its edge")
	res, err := mergeAccept(admin, proposal, true)
	if err != nil || res.Member.Role != string(domain.RoleAdmin) || res.Merge.RoleAfter != string(domain.RoleAdmin) {
		t.Fatalf("keep_admin accept = %+v, %v", res, err)
	}
	if m := serverInfoMember(t, tailnetDial(t, e, whois, tina.TailnetLogin)); m.ID != string(e.member.ID) || m.Role != string(domain.RoleAdmin) {
		t.Fatalf("tailnet login now reaches the server as %+v", m)
	}
}

// A member who is not an admin links their own GitHub account: the device
// opening the join link is told whose member it would act as and waits on
// the member's approval, which they give from their key.
func TestSelfLinkByNonAdmin(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	carolKey, carol := addMember(t, e, "Carol", domain.RoleViewer, false)
	c := controlAs(t, e, carolKey)
	link := e.invite(t, c, protocol.MethodMemberIdentityLink, protocol.MemberIdentityLinkParams{Login: "octo"})
	if link.Invitation.MemberID != string(carol.ID) || link.Invitation.Role != "" {
		t.Fatalf("link = %+v", link.Invitation)
	}
	laptop := newSigner(t)
	_, banner, err := e.dialEdge(t, grantFor(octo, laptop, "laptop"), laptop, e.edgeUser(t, octo))
	want := "This links your github account octo to the existing member Carol (" + string(carol.ID) + "), role viewer; you will act as that member."
	if err == nil || !strings.Contains(banner, want) {
		t.Fatalf("join by a member's link = %v, banner %q; want it to wait, saying %q", err, banner, want)
	}
	if m, lerr := identities(t, e).GetMemberByIdentity(context.Background(), octo.Provider, octo.Subject); !errors.Is(lerr, store.ErrNotFound) {
		t.Fatalf("the link bound the account before approval: %+v, %v", m, lerr)
	}
	dev, err := identities(t, e).GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{DeviceID: string(dev.ID)}, nil); err != nil {
		t.Fatalf("approve own link's device: %v", err)
	}
	if m := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))); m.ID != string(carol.ID) || m.Role != string(domain.RoleViewer) {
		t.Fatalf("linked device connects as %+v", m)
	}
	for _, login := range []string{"someone", "someone-else"} {
		raw, _ := json.Marshal(protocol.MemberIdentityLinkParams{Login: login})
		if _, perr := e.srv.dispatch(context.Background(), carol.ID, protocol.MethodMemberIdentityLink, raw); perr != nil {
			t.Fatalf("link %s: %v", login, perr)
		}
	}
	invs, err := identities(t, e).ListInvitations(context.Background())
	if err != nil || len(invs) != 1 || invs[0].Login != "someone-else" || invs[0].Member != carol.ID {
		t.Fatalf("open invitations = %+v, %v; want only Carol's newest link", invs, err)
	}
}

// An invite code grants what its issuer could, so it stops working once
// the issuer is no longer an admin, by member.role or a merge.
func TestInviteCodeDiesWithItsIssuersAdminRole(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "invites")
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = dir })
	_, zed := addMember(t, e, "Zed", domain.RoleAdmin, false)
	code, _, err := mintInvite(dir, time.Hour, zed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = controlClient(t, e).Call(protocol.MethodMemberRole,
		protocol.MemberRoleParams{MemberID: string(zed.ID), Role: string(domain.RoleViewer)}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := e.store.ListMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c, derr := e.dialAs("invite:"+code, nil, ssh.PublicKeys(newSigner(t))); derr == nil {
		_ = c.Wait()
	}
	if after, err := e.store.ListMembers(context.Background()); err != nil || len(after) != len(before) {
		t.Fatalf("members after joining with a demoted admin's code = %d, %v; want %d", len(after), err, len(before))
	}
}

// A merge that moved a home aside and never committed, because the server
// stopped, gets the home back at the next start.
func TestRecoverMergeArchivesPutsBackAnUncommittedHome(t *testing.T) {
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), filepath.Join(t.TempDir(), "caches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	merged := filepath.Join(t.TempDir(), "merged")
	e := newTestEnv(t, func(c *Config) { c.Homes, c.MergedDir = homes, merged })
	home, err := homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "notes.txt"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, _, err := e.srv.archiveHome("mrg-unfinished", e.member.ID)
	if err != nil || dir == "" {
		t.Fatalf("archive = %q, %v", dir, err)
	}
	if err = e.srv.RecoverMergeArchives(context.Background()); err != nil {
		t.Fatalf("RecoverMergeArchives: %v", err)
	}
	if data, rerr := os.ReadFile(filepath.Join(home, "notes.txt")); rerr != nil || string(data) != "kept" {
		t.Fatalf("home after recovery: %q, %v", data, rerr)
	}
	if _, serr := os.Stat(dir); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("archive left behind: %v", serr)
	}
	empty := filepath.Join(merged, "mrg-stopped-in-setup")
	if err = os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = e.srv.RecoverMergeArchives(context.Background()); err != nil {
		t.Fatalf("RecoverMergeArchives with an archive stopped before its marker: %v", err)
	}
	if _, err := os.Stat(empty); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty archive left behind: %v", err)
	}
}

// An invite code an absorbed admin minted keeps working when the merged
// member stays admin.
func TestInviteCodeFollowsAMergedIssuer(t *testing.T) {
	t.Parallel()
	e, _ := mergeEnv(t, nil)
	ctx := context.Background()
	ms := e.store.(store.MergeStore)
	tina := addTailnetMember(t, e, "tina@example.com", domain.RoleAdmin)
	now := time.Now().UTC()
	if err := ms.CreateMergeCode(ctx, tina.ID, "code", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	plan, err := ms.PlanMerge(ctx, "code", e.member.ID, "", now)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := ms.ProposeMerge(ctx, store.MergeProposal{CodeHash: "code", Proposer: e.member.ID, Keep: tina.ID,
		Digest: plan.Digest, Now: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ms.AcceptMerge(ctx, store.MergeAccept{ProposalID: pm.ProposalID, Caller: tina.ID, Digest: plan.Digest, Now: now}); err != nil {
		t.Fatal(err)
	}
	if admin, err := e.srv.inviteIssuerAdmin(ctx, e.member.ID); err != nil || !admin {
		t.Fatalf("issuer merged into an admin = %v, %v; want still an admin", admin, err)
	}
}

// A merge that demotes an admin revokes their invite codes for good:
// promoting the merged member again does not bring them back.
func TestDemotingMergeRevokesInviteCodes(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "invites")
	e, whois := mergeEnv(t, func(c *Config) { c.InvitesDir = dir })
	_, _ = addMember(t, e, "Zed", domain.RoleAdmin, false)
	code, _, err := mintInvite(dir, time.Hour, e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	tina := addTailnetMember(t, e, "tina@example.com", domain.RoleCollaborator)
	tc := tailnetDial(t, e, whois, tina.TailnetLogin)
	admin := controlClient(t, e)
	proposal := mustPropose(t, tc, mergeStart(t, admin), string(e.member.ID))
	if _, err = mergeAccept(admin, proposal, false); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if inviteUsable(dir, code) {
		t.Fatal("the demoted admin's invite code survived the merge")
	}
}

// Accepting a merge fences the control leases the two members hold and no
// one else's, and the devices waiting on their revoked account links are
// published as revoked.
func TestMergeFencesOnlyTheMembersLeasesAndDropsTheirLinks(t *testing.T) {
	t.Parallel()
	ctrl := control.New(control.Config{})
	e, whois := mergeEnv(t, func(c *Config) { c.Control = ctrl })
	e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	ctx := context.Background()
	tina := addTailnetMember(t, e, "tina@example.com", domain.RoleCollaborator)
	tc := tailnetDial(t, e, whois, tina.TailnetLogin)
	viKey, vi := addMember(t, e, "Vi", domain.RoleCollaborator, false)
	vc := controlAs(t, e, viKey)
	_, zed := addMember(t, e, "Zed", domain.RoleCollaborator, false)
	var runs []*domain.Run
	for range 2 {
		r := &domain.Run{WorkspaceID: e.ws.ID, MemberID: zed.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
		if err := e.store.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
	}
	if _, _, err := ctrl.Acquire(string(runs[0].ID), string(vi.ID), "vi-session", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctrl.Acquire(string(runs[1].ID), string(zed.ID), "zed-session", false); err != nil {
		t.Fatal(err)
	}
	e.invite(t, vc, protocol.MethodMemberIdentityLink, protocol.MemberIdentityLinkParams{Login: "octo"})
	laptop := newSigner(t)
	if _, _, err := e.dialEdge(t, grantFor(octo, laptop, "laptop"), laptop, e.edgeUser(t, octo)); err == nil {
		t.Fatal("a device joined through a link without approval")
	}
	waiting, err := identities(t, e).GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeMemberDevice}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close() //nolint:errcheck

	proposal := mustPropose(t, tc, mergeStart(t, vc), string(tina.ID))
	if _, err = mergeAccept(vc, proposal, false); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if ctrl.HeldByMember(string(runs[0].ID), vi.ID) {
		t.Fatal("the absorbed member's lease survived the merge")
	}
	if holder, ok := ctrl.Status(string(runs[1].ID)); !ok || holder.MemberID != zed.ID {
		t.Fatalf("another member's lease = %+v, %v; want it kept", holder, ok)
	}
	for {
		select {
		case ev := <-sub.Events():
			if p, ok := ev.Payload.(events.MemberDevicePayload); ok && p.DeviceID == waiting.ID && p.Status == domain.DeviceRevoked {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the device waiting on the revoked link was not published as revoked")
		}
	}
}
