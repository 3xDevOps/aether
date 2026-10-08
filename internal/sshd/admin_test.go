package sshd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
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
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = filepath.Join(t.TempDir(), "invites") })
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	c := controlAs(t, e, bob)

	var pe *protocol.Error
	if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: "x", Environment: protocol.WorkspaceEnvironment{}}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
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
	t.Parallel()
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
	if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: "x", Environment: protocol.WorkspaceEnvironment{}}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("pending workspace.add = %v, want CodeDenied", err)
	}
	if err := c.Call(protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: string(e.ws.ID)}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("pending workspace.get = %v, want CodeDenied", err)
	}
}

func TestAdminWorkspaceAddAndInvite(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "invites")
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = dir })
	c := controlClient(t, e)

	var ws protocol.WorkspaceAddResult
	if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: "other", Environment: protocol.WorkspaceEnvironment{}}, &ws); err != nil {
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
		Environment: protocol.WorkspaceEnvironment{},
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
	t.Parallel()
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

func TestMemberRemoveCleansTerminalAndHome(t *testing.T) {
	t.Parallel()
	homeRoot := t.TempDir()
	homes, err := memberhome.New(homeRoot, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) { c.Homes = homes })
	_, target := addMember(t, e, "Target", domain.RoleCollaborator, false)
	home, err := homes.Path(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "marker")
	if writeErr := os.WriteFile(marker, []byte("keep?"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	c := controlClient(t, e)
	if callErr := c.Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(target.ID)}, nil); callErr != nil {
		t.Fatalf("member.remove: %v", callErr)
	}
	if _, statErr := os.Stat(home); !os.IsNotExist(statErr) {
		t.Fatalf("member home still exists: stat err = %v", statErr)
	}
	reopened, err := memberhome.New(homeRoot, homes.CacheRoot(), nil)
	if err != nil {
		t.Fatal(err)
	}
	retryOwners, err := reopened.CacheMembers()
	if err != nil || !slices.Contains(retryOwners, target.ID) {
		t.Fatalf("removed member lost its saved-image cleanup retry key: %v, %v", retryOwners, err)
	}
	calls := e.runs.Calls()
	want := "terminal-stop:" + string(target.ID)
	if len(calls) == 0 || calls[len(calls)-1] != want {
		t.Fatalf("RunController calls = %v, want final %q", calls, want)
	}
}

func TestMemberRemovePreservesIdentityAndHomeWhenRetryMarkerCannotBeWritten(t *testing.T) {
	t.Parallel()
	homeRoot, cacheRoot, outside := t.TempDir(), t.TempDir(), t.TempDir()
	homes, err := memberhome.New(homeRoot, cacheRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) { c.Homes = homes })
	_, target := addMember(t, e, "Target", domain.RoleCollaborator, false)
	home, err := homes.Path(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(home, "credential")
	if writeErr := os.WriteFile(credential, []byte("preserved"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	poolParent := filepath.Join(cacheRoot, string(target.ID))
	if mkdirErr := os.MkdirAll(poolParent, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if symlinkErr := os.Symlink(outside, filepath.Join(poolParent, memberhome.CachePoolTerminal)); symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	err = controlClient(t, e).Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(target.ID)}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
		t.Fatalf("unsafe retry-marker storage did not refuse removal: %v", err)
	}
	if strings.Contains(pe.Message, cacheRoot) || strings.Contains(pe.Message, outside) {
		t.Fatalf("marker failure leaked private paths: %s", pe.Message)
	}
	if _, err := e.store.GetMember(t.Context(), target.ID); err != nil {
		t.Fatalf("member row lost before retry ownership persisted: %v", err)
	}
	if body, err := os.ReadFile(credential); err != nil || string(body) != "preserved" {
		t.Fatalf("home changed after marker refusal: %q, %v", body, err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("marker creation followed an unsafe pool link: %v, %v", entries, err)
	}
}

func TestMemberRemoveKeepsDeletedLegacyOwnerDiscoverableAfterHomeCleanupFailure(t *testing.T) {
	t.Parallel()
	homeRoot, cacheRoot := t.TempDir(), t.TempDir()
	cacheLockHeld := make(chan bool, 1)
	var homes *memberhome.Manager
	var target *domain.Member
	var err error
	homes, err = memberhome.New(homeRoot, cacheRoot, func(context.Context, string) error {
		unlock, available := homes.TryLockCaches(target.ID)
		if available {
			unlock()
		}
		cacheLockHeld <- !available
		return errors.New("home cleanup failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) { c.Homes = homes })
	_, target = addMember(t, e, "Legacy target", domain.RoleCollaborator, false)
	home, err := homes.Path(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if legacyWriteErr := os.WriteFile(filepath.Join(home, "legacy-install"), []byte("kept for retry"), 0o600); legacyWriteErr != nil {
		t.Fatal(legacyWriteErr)
	}
	if removeErr := controlClient(t, e).Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(target.ID)}, nil); removeErr != nil {
		t.Fatal(removeErr)
	}
	if !<-cacheLockHeld {
		t.Fatal("member home removal allowed concurrent cache provisioning")
	}
	if _, memberLookupErr := e.store.GetMember(t.Context(), target.ID); !errors.Is(memberLookupErr, store.ErrNotFound) {
		t.Fatalf("member deletion did not complete: %v", memberLookupErr)
	}
	reopened, err := memberhome.New(homeRoot, cacheRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	members, err := reopened.CacheMembers()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range members {
		found = found || id == target.ID
	}
	if !found {
		t.Fatalf("removed legacy member is no longer discoverable for home/image cleanup: %v", members)
	}
	info, err := reopened.ReadCache(target.ID, memberhome.CachePoolTerminal)
	if err != nil || info.MetadataError != "" || info.CleanupError == "" {
		t.Fatalf("durable cleanup retry cause lost: %+v, %v", info, err)
	}
	if body, err := os.ReadFile(filepath.Join(home, "legacy-install")); err != nil || string(body) != "kept for retry" {
		t.Fatalf("failed cleanup lost its retry target: %q, %v", body, err)
	}
}

func TestMemberRemoveAllowsRoomOnlyParticipant(t *testing.T) {
	e := newTestEnv(t, nil)
	_, target := addMember(t, e, "Room participant", domain.RoleCollaborator, false)
	rooms, ok := e.srv.cfg.Services.Rooms.(*persistentRoomService)
	if !ok {
		t.Fatal("room service is not persistent")
	}
	roomDB, ok := rooms.db.(interface {
		CreateRoomMessage(context.Context, *store.RoomMessage) error
		GetRoomMessage(context.Context, string) (*store.RoomMessage, error)
	})
	if !ok {
		t.Fatal("room service store has no room persistence")
	}
	message := &store.RoomMessage{
		WorkspaceID:    e.ws.ID,
		RunID:          e.run.ID,
		ActorID:        target.ID,
		Kind:           store.RoomMessageComment,
		Body:           "historical note",
		IdempotencyKey: "member-remove-room-only",
	}
	if err := roomDB.CreateRoomMessage(context.Background(), message); err != nil {
		t.Fatalf("CreateRoomMessage: %v", err)
	}

	if err := controlClient(t, e).Call(protocol.MethodMemberRemove,
		protocol.MemberRemoveParams{MemberID: string(target.ID)}, nil); err != nil {
		t.Fatalf("member.remove with room-only history: %v", err)
	}
	if _, err := e.store.GetMemberByPublicKey(context.Background(), target.PublicKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("public-key lookup after member.remove: %v, want ErrNotFound", err)
	}
	got, err := roomDB.GetRoomMessage(context.Background(), message.ID)
	if err != nil {
		t.Fatalf("GetRoomMessage after member.remove: %v", err)
	}
	if got.ActorID != target.ID || got.ActorDisplayName != target.DisplayName {
		t.Fatalf("room attribution after member.remove = %q/%q, want %q/%q",
			got.ActorID, got.ActorDisplayName, target.ID, target.DisplayName)
	}
}

func TestMemberRemoveRetainsMemberWhenTerminalCleanupFails(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	_, target := addMember(t, e, "Target", domain.RoleCollaborator, false)
	stopErr := errors.New("terminal is still using member credentials")
	e.runs.setTerminalStopErr(stopErr)

	c := controlClient(t, e)
	var pe *protocol.Error
	err := c.Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(target.ID)}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInternal ||
		!strings.Contains(pe.Message, "member.remove: stop terminal") {
		t.Fatalf("member.remove cleanup failure = %v, want contextual internal error", err)
	}
	if _, err := e.store.GetMember(context.Background(), target.ID); err != nil {
		t.Fatalf("member removed after terminal cleanup failure: %v", err)
	}
}

func TestInviteJoinRegistersCollaboratorAndBurns(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
