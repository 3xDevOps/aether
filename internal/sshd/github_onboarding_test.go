package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	githubservice "github.com/3xDevOps/Aether/internal/github"
	mirrorservice "github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// The provider boundary supplies a private organization repository readable by
// a collaborator, not owned by the connected account and not writable by it.
// Workspace persistence, mirror fetch/adoption, and transport authority remain real.
type githubRepositoryFixture struct {
	mu      sync.Mutex
	account githubservice.Account
	repo    githubservice.Repository
	err     error
	members []domain.MemberID
	pages   []int
}

func newGitHubRepositoryFixture() *githubRepositoryFixture {
	return &githubRepositoryFixture{
		account: githubservice.Account{ID: 42, Login: "octocat"},
		repo:    githubservice.Repository{ID: 123, FullName: "upstream/source", Name: "source", Private: true, DefaultBranch: "main", CloneURL: "https://github.com/upstream/source.git"},
	}
}

func (f *githubRepositoryFixture) Repositories(_ context.Context, member domain.MemberID, page int) (githubservice.RepositoryPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members = append(f.members, member)
	f.pages = append(f.pages, page)
	return githubservice.RepositoryPage{Account: f.account, Repositories: []githubservice.Repository{f.repo}, NextPage: page + 1}, f.err
}

func (f *githubRepositoryFixture) Repository(_ context.Context, member domain.MemberID, fullName string) (githubservice.Account, githubservice.Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members = append(f.members, member)
	if fullName != f.repo.FullName {
		return githubservice.Account{}, githubservice.Repository{}, errors.New("repository not accessible")
	}
	return f.account, f.repo, f.err
}

func (f *githubRepositoryFixture) setup(c *Config, mirrorConfig *mirrorservice.Config) {
	c.Services.GitHub = f
	mirrorConfig.GitHubCredentials = func(_ context.Context, member domain.MemberID) (string, int64, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.members = append(f.members, member)
		return "fixture-provider-credential", f.account.ID, f.err
	}
}

type githubOAuthFixture struct {
	mu       sync.Mutex
	members  []domain.MemberID
	sessions []string
}

func (f *githubOAuthFixture) StartGitHubOAuth(_ context.Context, member domain.MemberID) (protocol.GitHubOAuthResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members = append(f.members, member)
	return protocol.GitHubOAuthResult{State: "pending", SessionID: "attempt-1", UserCode: "ABCD-EFGH", VerificationURL: "https://github.com/login/device"}, nil
}

func (f *githubOAuthFixture) GitHubOAuthStatus(_ context.Context, member domain.MemberID, session string) (protocol.GitHubOAuthResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members = append(f.members, member)
	f.sessions = append(f.sessions, session)
	return protocol.GitHubOAuthResult{State: "pending", SessionID: session}, nil
}

func (f *githubOAuthFixture) CancelGitHubOAuth(_ context.Context, member domain.MemberID, session string) (protocol.GitHubOAuthResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members = append(f.members, member)
	f.sessions = append(f.sessions, session)
	return protocol.GitHubOAuthResult{State: "cancelled", SessionID: session}, nil
}

func TestGitHubOnboardingUsesTransportActorAcrossGateways(t *testing.T) {
	for _, transport := range []string{"ssh", "local"} {
		t.Run(transport, func(t *testing.T) {
			provider := newGitHubRepositoryFixture()
			oauth := &githubOAuthFixture{}
			e := newTestEnv(t, func(c *Config) {
				c.Services.GitHubOAuth = oauth
				c.Services.GitHub = provider
			})
			client := controlClient(t, e)
			call := func(method string, params json.RawMessage, result any) error {
				if transport == "ssh" {
					return client.Call(method, params, result)
				}
				raw, perr := e.srv.Local(e.member.ID).Call(t.Context(), method, params)
				if perr != nil {
					return perr
				}
				if result == nil {
					return nil
				}
				return json.Unmarshal(raw, result)
			}
			// Unknown credential-owner fields cannot impersonate another member.
			var started protocol.GitHubOAuthResult
			if err := call(protocol.MethodGitHubOAuthStart, json.RawMessage(`{"member_id":"victim"}`), &started); err != nil {
				t.Fatal(err)
			}
			if started.State != "pending" || started.SessionID != "attempt-1" || started.UserCode == "" {
				t.Fatalf("start = %+v", started)
			}
			for _, method := range []string{protocol.MethodGitHubOAuthStatus, protocol.MethodGitHubOAuthCancel} {
				if err := call(method, json.RawMessage(`{"member_id":"victim","session_id":"attempt-1"}`), nil); err != nil {
					t.Fatal(err)
				}
			}
			var page protocol.GitHubRepositoryListResult
			if err := call(protocol.MethodGitHubRepositoriesList, json.RawMessage(`{"member_id":"victim","page":2}`), &page); err != nil {
				t.Fatal(err)
			}
			if page.Account.ID != 42 || page.NextPage != 3 || len(page.Repositories) != 1 || !page.Repositories[0].Private || page.Repositories[0].CanPush {
				t.Fatalf("repository page = %+v", page)
			}
			oauth.mu.Lock()
			defer oauth.mu.Unlock()
			if len(oauth.members) != 3 || strings.Join(oauth.sessions, ",") != "attempt-1,attempt-1" {
				t.Fatalf("OAuth calls = %v %v", oauth.members, oauth.sessions)
			}
			for _, member := range oauth.members {
				if member != e.member.ID {
					t.Fatalf("forged actor reached OAuth: %s", member)
				}
			}
			provider.mu.Lock()
			defer provider.mu.Unlock()
			if len(provider.members) != 1 || provider.members[0] != e.member.ID || provider.pages[0] != 2 {
				t.Fatalf("provider caller = %v pages=%v", provider.members, provider.pages)
			}
		})
	}
}

func TestGitHubOnboardingRejectsNonadminAcrossGateways(t *testing.T) {
	for _, role := range []domain.Role{domain.RoleCollaborator, domain.RoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			provider := newGitHubRepositoryFixture()
			oauth := &githubOAuthFixture{}
			e := newTestEnv(t, func(c *Config) {
				c.Services.GitHubOAuth = oauth
				c.Services.GitHub = provider
			})
			signer, member := addMember(t, e, "Nonadmin", role, false)
			client := controlAs(t, e, signer)
			for _, method := range []string{protocol.MethodGitHubOAuthStart, protocol.MethodGitHubOAuthStatus, protocol.MethodGitHubOAuthCancel, protocol.MethodGitHubRepositoriesList, protocol.MethodWorkspaceImport} {
				params := json.RawMessage(fmt.Sprintf(`{"member_id":%q,"session_id":"attempt-1","source_url":"https://github.com/upstream/source.git","auth":"github"}`, e.member.ID))
				wantDenied(t, client.Call(method, params, nil), method)
				_, perr := e.srv.Local(member.ID).Call(t.Context(), method, params)
				if perr == nil || perr.Code != protocol.CodeDenied {
					t.Fatalf("local %s = %v", method, perr)
				}
			}
			if len(oauth.members) != 0 || len(provider.members) != 0 {
				t.Fatal("denied caller reached GitHub provider")
			}
		})
	}
}

func TestGitHubImportBindsPrivateCollaboratorAndRecoversSameWorkspace(t *testing.T) {
	provider := newGitHubRepositoryFixture()
	e, engine, source, _ := workspaceImportEnv(t, true, provider.setup)
	client := controlClient(t, e)
	p := workspaceImportParams("private-collaborator")
	p.Auth, p.BaseBranch, p.GitHubAccountID = "github", "missing", 42
	var imported protocol.WorkspaceImportResult
	if err := client.Call(protocol.MethodWorkspaceImport, p, &imported); err != nil {
		t.Fatal(err)
	}
	if !imported.Created || imported.Workspace.ID == "" || imported.Error == "" || !strings.Contains(imported.Error, "missing") || !imported.Mirror.Enabled || imported.Mirror.AcceptedCommit != "" {
		t.Fatalf("failed import lost identity: %+v", imported)
	}
	if imported.Workspace.Origin != "" || imported.Mirror.GitHubMemberID != string(e.member.ID) || imported.Mirror.GitHubUserID != 42 || imported.Mirror.PublicKey != "" {
		t.Fatalf("wrong binding/origin: %+v", imported)
	}
	if got := strings.Join(e.runs.Calls(), ","); got != "terminal:"+string(e.member.ID)+",github-connect:"+string(e.member.ID) {
		t.Fatalf("native setup = %s", got)
	}
	workspaceImportGit(t, source, "branch", "missing", "HEAD")
	var refreshed protocol.WorkspaceMirrorResult
	params := protocol.WorkspaceMirrorParams{WorkspaceID: imported.Workspace.ID}
	if err := client.Call(protocol.MethodWorkspaceMirrorRefresh, params, &refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed.Generation != imported.Mirror.Generation || refreshed.ObservedCommit == "" || refreshed.AcceptedCommit != "" {
		t.Fatalf("refresh changed identity or adopted: %+v", refreshed)
	}
	if _, err := engine.WorkspaceBranchCommit(t.Context(), domain.WorkspaceID(imported.Workspace.ID), "missing"); err == nil {
		t.Fatal("refresh silently adopted")
	}
	if err := client.Call(protocol.MethodWorkspaceMirrorAdopt, protocol.WorkspaceMirrorAdoptParams{WorkspaceID: imported.Workspace.ID, Generation: refreshed.Generation + 1, ExpectedCommit: refreshed.ObservedCommit}, nil); err == nil {
		t.Fatal("stale adoption accepted")
	}
	if err := client.Call(protocol.MethodWorkspaceMirrorAdopt, protocol.WorkspaceMirrorAdoptParams{WorkspaceID: imported.Workspace.ID, Generation: refreshed.Generation, ExpectedCommit: refreshed.ObservedCommit}, nil); err != nil {
		t.Fatal(err)
	}
	workspaces, err := e.store.ListWorkspaces(t.Context())
	if err != nil || len(workspaces) != 2 {
		t.Fatalf("recovery created duplicate workspace: %d %v", len(workspaces), err)
	}
}

func TestGitHubImportPrecreationFailures(t *testing.T) {
	for _, cause := range []string{"account", "access", "empty", "source", "setup"} {
		t.Run(cause, func(t *testing.T) {
			provider := newGitHubRepositoryFixture()
			p := workspaceImportParams("must-not-exist")
			p.Auth, p.GitHubAccountID = "github", 42
			switch cause {
			case "account":
				p.GitHubAccountID = 99
			case "access":
				provider.err = errors.New("repository not accessible")
			case "empty":
				provider.repo.DefaultBranch = ""
			case "source":
				p.SourceURL = "https://github.com.evil.test/upstream/source.git"
			}
			e, _, _, _ := workspaceImportEnv(t, true, provider.setup)
			if cause == "setup" {
				e.runs.setErr(errors.New("native signing setup failed"))
			}
			if err := controlClient(t, e).Call(protocol.MethodWorkspaceImport, p, nil); err == nil {
				t.Fatal("invalid import accepted")
			}
			workspaces, err := e.store.ListWorkspaces(t.Context())
			if err != nil || len(workspaces) != 1 {
				t.Fatalf("precreation error created workspace: %d %v", len(workspaces), err)
			}
		})
	}
}

func TestGitHubMirrorRebindUsesCurrentAdminAndAccount(t *testing.T) {
	provider := newGitHubRepositoryFixture()
	e, _, _, _ := workspaceImportEnv(t, true, provider.setup)
	first := controlClient(t, e)
	p := workspaceImportParams("rebind")
	p.Auth, p.BaseBranch, p.GitHubAccountID = "github", "", 42
	var imported protocol.WorkspaceImportResult
	if err := first.Call(protocol.MethodWorkspaceImport, p, &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Error != "" || imported.Workspace.BaseBranch != "main" {
		t.Fatalf("default branch = %+v", imported)
	}
	signer, other := addMember(t, e, "Other admin", domain.RoleAdmin, false)
	provider.mu.Lock()
	provider.account = githubservice.Account{ID: 84, Login: "other"}
	provider.mu.Unlock()
	second := controlAs(t, e, signer)
	params := map[string]any{"workspace_id": imported.Workspace.ID, "source_url": p.SourceURL, "auth": "github", "github_account_id": 42, "github_member_id": e.member.ID, "github_user_id": 42}
	if err := second.Call(protocol.MethodWorkspaceMirrorConfigure, params, nil); err == nil {
		t.Fatal("changed account expectation accepted")
	}
	var prior protocol.WorkspaceMirrorResult
	if err := first.Call(protocol.MethodWorkspaceMirrorStatus, protocol.WorkspaceMirrorParams{WorkspaceID: imported.Workspace.ID}, &prior); err != nil {
		t.Fatal(err)
	}
	if prior.Generation != imported.Mirror.Generation || prior.GitHubUserID != 42 {
		t.Fatalf("failed rebind mutated mirror: %+v", prior)
	}
	params["github_account_id"] = 84
	var rebound protocol.WorkspaceMirrorResult
	if err := second.Call(protocol.MethodWorkspaceMirrorConfigure, params, &rebound); err != nil {
		t.Fatal(err)
	}
	if rebound.GitHubMemberID != string(other.ID) || rebound.GitHubUserID != 84 || rebound.Generation <= prior.Generation || rebound.AcceptedCommit != "" {
		t.Fatalf("rebind trusted forged owner or adopted: %+v", rebound)
	}
}

func TestGitHubOnboardingOptionalServicesUnavailable(t *testing.T) {
	e := newTestEnv(t, nil)
	client := controlClient(t, e)
	for _, method := range []string{protocol.MethodGitHubOAuthStart, protocol.MethodGitHubOAuthStatus, protocol.MethodGitHubOAuthCancel, protocol.MethodGitHubRepositoriesList} {
		err := client.Call(method, protocol.GitHubOAuthCancelParams{SessionID: "attempt"}, nil)
		if pe := wireErrOf(t, err); pe.Code != protocol.CodeUnavailable {
			t.Fatalf("%s = %v, want unavailable", method, pe)
		}
	}
}

type githubConnectFailure struct{ RunController }

func (f githubConnectFailure) ConnectGitHub(context.Context, domain.MemberID) (domain.GitHubConnection, error) {
	return domain.GitHubConnection{}, errors.New("native signing setup failed")
}

func TestGitHubImportNativeConnectionFailurePrecedesCreation(t *testing.T) {
	provider := newGitHubRepositoryFixture()
	e, _, _, _ := workspaceImportEnv(t, true, provider.setup, func(c *Config, _ *mirrorservice.Config) {
		c.Runs = githubConnectFailure{RunController: c.Runs}
	})
	p := workspaceImportParams("not-created")
	p.Auth, p.GitHubAccountID = "github", 42
	err := controlClient(t, e).Call(protocol.MethodWorkspaceImport, p, nil)
	if err == nil || !strings.Contains(err.Error(), "native signing setup failed") {
		t.Fatalf("connect failure = %v", err)
	}
	workspaces, err := e.store.ListWorkspaces(t.Context())
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("native setup failure created workspace: %d %v", len(workspaces), err)
	}
	if got := strings.Join(e.runs.Calls(), ","); got != "terminal:"+string(e.member.ID) {
		t.Fatalf("Environment not ensured before native connection: %s", got)
	}
}

func TestGitHubAdditionalRepositoryReusesConnectionAndExplicitOrigin(t *testing.T) {
	provider := newGitHubRepositoryFixture()
	e, _, _, _ := workspaceImportEnv(t, true, provider.setup)
	client := controlClient(t, e)
	p := workspaceImportParams("first")
	p.Auth, p.BaseBranch, p.GitHubAccountID = "github", "", 42
	var first, second protocol.WorkspaceImportResult
	if err := client.Call(protocol.MethodWorkspaceImport, p, &first); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.repo.ID = 456
	provider.repo.FullName, provider.repo.Name = "another-org/another-repo", "another-repo"
	provider.repo.CloneURL = "https://github.com/another-org/another-repo.git"
	provider.mu.Unlock()
	p.Name, p.SourceURL, p.Origin = "second", "https://github.com/another-org/another-repo.git", "https://github.com/octocat/publish.git"
	if err := client.Call(protocol.MethodWorkspaceImport, p, &second); err != nil {
		t.Fatal(err)
	}
	if first.Error != "" || second.Error != "" || first.Workspace.ID == second.Workspace.ID ||
		first.Mirror.GitHubMemberID != second.Mirror.GitHubMemberID || second.Mirror.GitHubUserID != 42 ||
		second.Workspace.Origin != p.Origin || second.Mirror.SourceURL != p.SourceURL || second.Mirror.AcceptedCommit != "" {
		t.Fatalf("additional repository = %+v / %+v", first, second)
	}
	setup := "terminal:" + string(e.member.ID) + ",github-connect:" + string(e.member.ID)
	if got := strings.Join(e.runs.Calls(), ","); got != setup+","+setup {
		t.Fatalf("additional repository requested new authentication instead of native connection setup: %s", got)
	}
}
