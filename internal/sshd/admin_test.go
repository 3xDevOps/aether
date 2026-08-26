package sshd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func addMember(t *testing.T, e *testEnv, name string, role domain.Role, pending bool) (ssh.Signer, *domain.Member) {
	t.Helper()
	signer := newSigner(t)
	m := &domain.Member{
		DisplayName: name,
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#3cb44b",
		Role:        role,
		Pending:     pending,
	}
	if err := e.store.CreateMember(context.Background(), m); err != nil {
		t.Fatalf("create member: %v", err)
	}
	return signer, m
}

func controlAs(t *testing.T, e *testEnv, signer ssh.Signer) *protocol.Client {
	t.Helper()
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return controlClientOn(t, client)
}

func TestAdminRPCDeniedForNonAdmin(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = filepath.Join(t.TempDir(), "invites") })
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	c := controlAs(t, e, bob)

	var pe *protocol.Error
	if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: "x", Environment: protocol.WorkspaceEnvironment{CustomImage: "img"}}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator workspace.add = %v, want CodeDenied", err)
	}
	if err := c.Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator member.invite = %v, want CodeDenied", err)
	}
	if err := c.Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(e.member.ID)}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator member.remove = %v, want CodeDenied", err)
	}
}

func TestPendingDeniedExceptServerInfo(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = filepath.Join(t.TempDir(), "invites") })
	pat, pending := addMember(t, e, "Pat", domain.RoleCollaborator, true)
	c := controlAs(t, e, pat)

	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatalf("pending server.info: %v", err)
	}
	if info.Member.ID != string(pending.ID) || !info.Member.Pending {
		t.Errorf("server.info member = %+v", info.Member)
	}

	var pe *protocol.Error
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("pending workspace.list = %v, want CodeDenied", err)
	}
	if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: "x", Environment: protocol.WorkspaceEnvironment{CustomImage: "img"}}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("pending workspace.add = %v, want CodeDenied", err)
	}
	if err := c.Call(protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: string(e.ws.ID)}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("pending workspace.get = %v, want CodeDenied", err)
	}
}

func TestAdminWorkspaceAddAndInvite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "invites")
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = dir })
	c := controlClient(t, e)

	var ws protocol.WorkspaceAddResult
	if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: "other", Environment: protocol.WorkspaceEnvironment{CustomImage: "alpine"}}, &ws); err != nil {
		t.Fatalf("workspace.add: %v", err)
	}
	if ws.Workspace.Name != "other" || ws.Workspace.ID == "" {
		t.Errorf("workspace = %+v", ws.Workspace)
	}
	// An omitted base_branch takes the server default rather than landing
	// empty: every run branches off it.
	if ws.Workspace.BaseBranch != domain.DefaultBaseBranch {
		t.Errorf("base_branch = %q, want %q", ws.Workspace.BaseBranch, domain.DefaultBaseBranch)
	}

	var pinned protocol.WorkspaceAddResult
	if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{
		Name: "pinned", BaseBranch: "trunk",
		Environment: protocol.WorkspaceEnvironment{CustomImage: "alpine"},
	}, &pinned); err != nil {
		t.Fatalf("workspace.add with base_branch: %v", err)
	}
	if pinned.Workspace.BaseBranch != "trunk" {
		t.Errorf("base_branch = %q, want trunk", pinned.Workspace.BaseBranch)
	}

	var inv protocol.MemberInviteResult
	if err := c.Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{}, &inv); err != nil {
		t.Fatalf("member.invite: %v", err)
	}
	if !isInviteCode(inv.Code) || inv.ExpiresAt == "" {
		t.Errorf("invite = %+v", inv)
	}
	if _, err := time.Parse(time.RFC3339, inv.ExpiresAt); err != nil {
		t.Errorf("expires_at: %v", err)
	}
	if !inviteUsable(dir, inv.Code) {
		t.Fatal("minted invite not on disk")
	}
}

func TestRefuseDeletingLastAdmin(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	var pe *protocol.Error
	if err := c.Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(e.member.ID)}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("delete last admin = %v, want CodeDenied", err)
	}
	if pe != nil && !strings.Contains(pe.Message, "last admin") {
		t.Errorf("message %q does not mention last admin", pe.Message)
	}
}

func TestInviteJoinRegistersCollaboratorAndBurns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "invites")
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = dir })
	code, _, err := mintInvite(dir, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	joiner := newSigner(t)
	client, err := e.dialAs("invite:"+code+":Jo", nil, ssh.PublicKeys(joiner))
	if err != nil {
		t.Fatalf("invite dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	m := serverInfoMember(t, controlClientOn(t, client))
	if m.Role != string(domain.RoleCollaborator) {
		t.Errorf("role = %q, want collaborator", m.Role)
	}
	if m.Pending {
		t.Error("invite join must not land pending")
	}
	if m.DisplayName != "Jo" {
		t.Errorf("display = %q, want Jo", m.DisplayName)
	}
	if inviteUsable(dir, code) {
		t.Fatal("invite code was not burned")
	}

	var banner strings.Builder
	if c, err := e.dialAs("invite:"+code, &banner, ssh.PublicKeys(newSigner(t))); err == nil {
		_ = c.Close()
		t.Fatal("reused invite code was accepted")
	}
}

func TestInviteProbeIsSideEffectFree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "invites")
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = dir })
	code, _, err := mintInvite(dir, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	key := newSigner(t).PublicKey()
	perms, err := e.srv.authenticate(userMeta{user: "invite:" + code}, key)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if perms.Extensions[inviteCodeExtension] != code {
		t.Errorf("invite extension = %q", perms.Extensions[inviteCodeExtension])
	}
	if perms.Extensions[memberIDExtension] != "" {
		t.Error("probe minted a member ID")
	}
	members, err := e.store.ListMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Errorf("probe created a member: %d rows", len(members))
	}
	if !inviteUsable(dir, code) {
		t.Fatal("probe burned the invite")
	}
}

type userMeta struct {
	user string
	ssh.ConnMetadata
}

func (u userMeta) User() string { return u.user }
