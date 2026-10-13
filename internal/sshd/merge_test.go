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

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// mergeEnv is a server whose seeded run is finished, with a tailnet
// resolver that answers only while tailnetDial dials.
func mergeEnv(t *testing.T, policy edgeproto.AccessPolicy, mod func(*Config)) (*testEnv, *fakeWhoIs) {
	t.Helper()
	whois := &fakeWhoIs{err: errors.New("not on the tailnet")}
	e := newTestEnv(t, func(c *Config) {
		c.WhoIs, c.EdgeAccess = whois, policy
		if mod != nil {
			mod(c)
		}
	})
	if err := e.store.UpdateRunStatus(context.Background(), e.run.ID, domain.RunCompleted, "", nil, nil); err != nil {
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
	return mergeStartKeepingAdmin(t, c, false)
}

func mergeStartKeepingAdmin(t *testing.T, c *protocol.Client, keepAdmin bool) string {
	t.Helper()
	var res protocol.MemberMergeStartResult
	if err := c.Call(protocol.MethodMemberMergeStart, protocol.MemberMergeStartParams{KeepAdmin: keepAdmin}, &res); err != nil {
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

func mergeConfirm(c *protocol.Client, code, keep, digest string, keepAdmin bool) (protocol.MemberMergeConfirmResult, error) {
	var res protocol.MemberMergeConfirmResult
	err := c.Call(protocol.MethodMemberMergeConfirm, protocol.MemberMergeConfirmParams{Code: code, KeepSettingsOf: keep,
		PlanDigest: digest, DiscardAck: true, KeepAdmin: keepAdmin}, &res)
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
// as two members merges them: the code minted on the tailnet sign-in,
// confirmed from the GitHub one. The GitHub device then resolves to the
// survivor, the absorbed member's connection is closed, its home is
// archived, and a replay returns the recorded merge.
func TestMergeTailnetMemberWithEdgeMember(t *testing.T) {
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), filepath.Join(t.TempDir(), "caches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	merged := filepath.Join(t.TempDir(), "merged")
	e, whois := mergeEnv(t, edgeproto.PolicyAccount, func(c *Config) { c.Homes, c.MergedDir = homes, merged })
	tina := addTailnetMember(t, e, "octo@example.com", domain.RoleCollaborator)
	tailnet := tailnetDial(t, e, whois, tina.TailnetLogin)

	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop := newSigner(t)
	edgeConn := e.mustDialEdge(t, octo, laptop, "laptop")
	github := controlClientOn(t, edgeConn)
	gh := serverInfoMember(t, github)
	if gh.ID == string(tina.ID) {
		t.Fatal("the edge sign-in is already the tailnet member")
	}
	ghHome, err := homes.Path(domain.MemberID(gh.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghHome, "notes.txt"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}

	code := mergeStart(t, tailnet)
	plan := mergePreview(t, github, code)
	if plan.Minted.ID != string(tina.ID) || plan.Confirming.ID != gh.ID || plan.Role != string(domain.RoleCollaborator) ||
		plan.RoleWithKeepAdmin != "" || len(plan.Choices) != 2 || plan.PlanDigest == "" {
		t.Fatalf("plan = %+v", plan)
	}
	if !slices.Contains(plan.SignIns, "tailnet login octo@example.com") || !slices.Contains(plan.SignIns, "github account octo at the edge") {
		t.Fatalf("sign-ins = %v", plan.SignIns)
	}
	if _, err := mergeConfirm(github, code, string(tina.ID), "0000", false); err == nil || !strings.Contains(err.Error(), "preview again") {
		t.Fatalf("confirm with a stale digest = %v", err)
	}
	res, err := mergeConfirm(github, code, string(tina.ID), plan.PlanDigest, false)
	if err != nil {
		t.Fatalf("member.merge.confirm: %v", err)
	}
	if res.Member.ID != string(tina.ID) || res.Merge.AbsorbedID != gh.ID || res.Merge.Discarded.DisplayName != "Octo Cat" ||
		res.Merge.ConfirmedByKind != "edge-device" || res.Merge.ArchivePath != filepath.Join(merged, res.Merge.ID) {
		t.Fatalf("merge = %+v", res)
	}
	if data, err := os.ReadFile(filepath.Join(res.Merge.ArchivePath, "home", "notes.txt")); err != nil || string(data) != "kept" {
		t.Fatalf("archived home: %q, %v", data, err)
	}
	waitClosed(t, edgeConn)

	if m := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))); m.ID != string(tina.ID) {
		t.Fatalf("the GitHub device reconnects as %s, want the survivor %s", m.ID, tina.ID)
	}
	if m := serverInfoMember(t, tailnetDial(t, e, whois, tina.TailnetLogin)); m.ID != string(tina.ID) {
		t.Fatalf("the tailnet login reconnects as %s", m.ID)
	}
	if entries, err := e.srv.EdgeDirectory(context.Background()); err != nil || len(entries) != 1 || entries[0].Subject != octo.Subject {
		t.Fatalf("edge directory = %+v, %v", entries, err)
	}
	replay, err := mergeConfirm(tailnet, code, string(tina.ID), plan.PlanDigest, false)
	if err != nil || replay.Merge.ID != res.Merge.ID {
		t.Fatalf("replayed confirm = %+v, %v", replay, err)
	}
	_, err = mergeConfirm(controlClient(t, e), code, string(e.member.ID), plan.PlanDigest, false)
	wantRefusal(t, err, "already used")
	var list protocol.MemberMergeListResult
	if err := tailnet.Call(protocol.MethodMemberMergeList, struct{}{}, &list); err != nil || len(list.Merges) != 1 || list.Merges[0].ID != res.Merge.ID {
		t.Fatalf("member.merge.list = %+v, %v", list, err)
	}
}

func TestMergeRefusals(t *testing.T) {
	t.Parallel()
	e, whois := mergeEnv(t, edgeproto.PolicyAccount, nil)
	ctx := context.Background()
	admin := controlClient(t, e)
	viewerKey, viewer := addMember(t, e, "Vi", domain.RoleViewer, false)
	vi := controlAs(t, e, viewerKey)

	wantRefusal(t, vi.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: mergeStart(t, vi)}, nil),
		"already the same member")

	// Both members have an SSH key.
	wantRefusal(t, vi.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: mergeStart(t, admin)}, nil),
		"both members have an SSH public key")

	// A pending member gets nothing; approving it lets the plan through to
	// the next refusal.
	pending := addTailnetMember(t, e, "pat@example.com", domain.RoleViewer)
	pending.Pending = true
	if err := e.store.UpdateMember(ctx, pending); err != nil {
		t.Fatal(err)
	}
	pat := tailnetDial(t, e, whois, pending.TailnetLogin)
	wantRefusal(t, pat.Call(protocol.MethodMemberMergeStart, struct{}{}, nil), "pending")

	// A live run of either member.
	tina := addTailnetMember(t, e, "tina@example.com", domain.RoleCollaborator)
	tc := tailnetDial(t, e, whois, tina.TailnetLogin)
	run := &domain.Run{WorkspaceID: e.ws.ID, MemberID: viewer.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := e.store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	code := mergeStart(t, vi)
	wantRefusal(t, tc.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: code}, nil), string(run.ID))
	if err := e.store.UpdateRunStatus(ctx, run.ID, domain.RunFailed, "", nil, nil); err != nil {
		t.Fatal(err)
	}

	// The result role is the lower one; the viewer's code keeps it a viewer.
	plan := mergePreview(t, tc, code)
	if plan.Role != string(domain.RoleViewer) || plan.RoleWithKeepAdmin != "" {
		t.Fatalf("plan role = %s, keep-admin role %q", plan.Role, plan.RoleWithKeepAdmin)
	}
	if _, err := mergeConfirm(tc, code, string(tina.ID), plan.PlanDigest, true); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if m, _ := e.store.GetMember(ctx, tina.ID); m.Role != domain.RoleViewer || m.PublicKey != viewer.PublicKey {
		t.Fatalf("merged member = %+v; keep_admin may not lift a merge of two non-admins", m)
	}

	// Expired code.
	hash, _ := mergeCodeHash("AAAA-BBBB-CCCC-DDDD")
	if err := e.store.(store.MergeStore).CreateMergeCode(ctx, e.member.ID, hash, false, time.Now().Add(-time.Hour), time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, tc.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: "aaaa bbbb cccc dddd"}, nil), "expired")
	wantRefusal(t, tc.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: "AAAA-BBBB-CCCC-EEEE"}, nil), "unknown")
}

// Merging the server's only admin into a collaborator demotes it unless
// keep_admin is set, and keep_admin counts only when the admin side asks
// for it: here the admin minted the code, so only a code minted with
// keep_admin lets the collaborator's confirm keep the merged member admin.
func TestMergeLastAdminNeedsKeepAdmin(t *testing.T) {
	t.Parallel()
	e, whois := mergeEnv(t, edgeproto.PolicyAccount, nil)
	tina := addTailnetMember(t, e, "tina@example.com", domain.RoleCollaborator)
	tc := tailnetDial(t, e, whois, tina.TailnetLogin)
	admin := controlClient(t, e)
	wantRefusal(t, tc.Call(protocol.MethodMemberMergeStart, protocol.MemberMergeStartParams{KeepAdmin: true}, nil), "requires the admin role")

	code := mergeStart(t, admin)
	plan := mergePreview(t, tc, code)
	if plan.Role != string(domain.RoleCollaborator) || plan.RoleWithKeepAdmin != string(domain.RoleAdmin) || !plan.KeepAdminRequired || plan.KeepAdminConsented {
		t.Fatalf("plan = %+v", plan)
	}
	_, err := mergeConfirm(tc, code, string(e.member.ID), plan.PlanDigest, false)
	wantRefusal(t, err, "last admin")
	_, err = mergeConfirm(tc, code, string(e.member.ID), plan.PlanDigest, true)
	wantRefusal(t, err, "must come from the admin's sign-in")

	code = mergeStartKeepingAdmin(t, admin, true)
	if plan = mergePreview(t, tc, code); !plan.KeepAdminConsented {
		t.Fatalf("plan of a code minted with keep_admin = %+v", plan)
	}
	res, err := mergeConfirm(tc, code, string(e.member.ID), plan.PlanDigest, true)
	if err != nil || res.Member.Role != string(domain.RoleAdmin) || res.Merge.RoleAfter != string(domain.RoleAdmin) {
		t.Fatalf("keep_admin confirm = %+v, %v", res, err)
	}
	if m := serverInfoMember(t, tailnetDial(t, e, whois, tina.TailnetLogin)); m.ID != string(e.member.ID) || m.Role != string(domain.RoleAdmin) {
		t.Fatalf("tailnet login now reaches the server as %+v", m)
	}
}

// Under approved-devices a member who is not an admin links their own
// GitHub account: the join link opened with that account waits on the
// member's approval, which they give from their key.
func TestSelfLinkByNonAdmin(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	carolKey, carol := addMember(t, e, "Carol", domain.RoleViewer, false)
	c := controlAs(t, e, carolKey)
	link := e.invite(t, c, protocol.MethodMemberIdentityLink, protocol.MemberIdentityLinkParams{Login: "octo"})
	if link.Invitation.MemberID != string(carol.ID) || link.Invitation.Role != "" {
		t.Fatalf("link = %+v", link.Invitation)
	}
	laptop := newSigner(t)
	code := e.refusedForApproval(t, octo, laptop, "laptop")
	if err := approveByCode(c, code, nil); err != nil {
		t.Fatalf("approve own link's device: %v", err)
	}
	if m := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))); m.ID != string(carol.ID) || m.Role != string(domain.RoleViewer) {
		t.Fatalf("linked device connects as %+v", m)
	}
	raw, _ := json.Marshal(protocol.MemberIdentityLinkParams{Login: "someone"})
	if _, perr := e.srv.dispatch(context.Background(), carol.ID, protocol.MethodMemberIdentityLink, raw); perr != nil {
		t.Fatalf("second link: %v", perr)
	}
	invs, err := identities(t, e).ListInvitations(context.Background())
	if err != nil || len(invs) != 1 || invs[0].Login != "someone" || invs[0].Member != carol.ID {
		t.Fatalf("open invitations = %+v, %v; want only Carol's newest link", invs, err)
	}
}
