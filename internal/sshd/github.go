package sshd

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	githubservice "github.com/3xDevOps/Aether/internal/github"
	mirrorservice "github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodGitHubConnect, (*Server).githubConnect)
	registerMethod(protocol.MethodGitHubProbe, (*Server).githubProbe)
	registerGuarded(protocol.MethodGitHubOAuthStart, permissions.WorkspaceAdmin, nil, (*Server).githubOAuthStart)
	registerGuarded(protocol.MethodGitHubOAuthStatus, permissions.WorkspaceAdmin, nil, (*Server).githubOAuthStatus)
	registerGuarded(protocol.MethodGitHubOAuthCancel, permissions.WorkspaceAdmin, nil, (*Server).githubOAuthCancel)
	registerGuarded(protocol.MethodGitHubRepositoriesList, permissions.WorkspaceAdmin, nil, (*Server).githubRepositoriesList)
}

// githubConnect finishes the caller's own GitHub connection. It is member
// scoped for the same reason the environment terminal is: the login it
// completes lives in that member's container and nowhere else.
func (s *Server) githubConnect(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	conn, err := s.cfg.Runs.ConnectGitHub(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.GitHubConnectResult{
		Login:       conn.Login,
		SigningKey:  conn.SigningKey,
		Fingerprint: conn.Fingerprint,
	}, nil
}

// githubProbe reports the gh in the caller's own environment terminal, so
// the dashboard can say what is wrong before it prints a login command that
// container cannot run.
func (s *Server) githubProbe(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	cli, err := s.cfg.Runs.ProbeGitHubCLI(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.GitHubProbeResult{
		Status:      string(cli.Status),
		Version:     cli.Version,
		Minimum:     cli.Minimum,
		Detail:      cli.Detail,
		Image:       cli.Image,
		SavedImage:  cli.SavedImage,
		Path:        cli.Path,
		Remedy:      cli.Remedy,
		AdminRemedy: cli.AdminRemedy,
	}, nil
}

// GitHubOAuthController is the optional browser authorization service, separate
// from run control and its decorators. The authenticated transport supplies member.
type GitHubOAuthController interface {
	StartGitHubOAuth(context.Context, domain.MemberID) (protocol.GitHubOAuthResult, error)
	GitHubOAuthStatus(context.Context, domain.MemberID, string) (protocol.GitHubOAuthResult, error)
	CancelGitHubOAuth(context.Context, domain.MemberID, string) (protocol.GitHubOAuthResult, error)
}

// GitHubService deliberately excludes credential access from RPC handlers.
type GitHubService interface {
	Repositories(context.Context, domain.MemberID, int) (githubservice.RepositoryPage, error)
	Repository(context.Context, domain.MemberID, string) (githubservice.Account, githubservice.Repository, error)
}

func (s *Server) githubOAuthStart(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if _, err := decodeParams[struct{}](params); err != nil {
		return nil, err
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodGitHubOAuthStart); err != nil {
		return nil, err
	}
	controller := s.cfg.Services.GitHubOAuth
	if controller == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "GitHub authorization is not available"}
	}
	result, err := controller.StartGitHubOAuth(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return result, nil
}

func (s *Server) githubOAuthStatus(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.GitHubOAuthStatusParams](params)
	if perr != nil {
		return nil, perr
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodGitHubOAuthStatus); err != nil {
		return nil, err
	}
	controller := s.cfg.Services.GitHubOAuth
	if controller == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "GitHub authorization is not available"}
	}
	result, err := controller.GitHubOAuthStatus(ctx, member, p.SessionID)
	if err != nil {
		return nil, rpcError(err)
	}
	return result, nil
}

func (s *Server) githubOAuthCancel(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.GitHubOAuthCancelParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.SessionID == "" {
		return nil, invalidParams("session_id is required")
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodGitHubOAuthCancel); err != nil {
		return nil, err
	}
	controller := s.cfg.Services.GitHubOAuth
	if controller == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "GitHub authorization is not available"}
	}
	result, err := controller.CancelGitHubOAuth(ctx, member, p.SessionID)
	if err != nil {
		return nil, rpcError(err)
	}
	return result, nil
}

func (s *Server) githubRepositoriesList(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.GitHubRepositoriesListParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Page < 0 {
		return nil, invalidParams("page must be greater than zero")
	}
	if p.Page == 0 {
		p.Page = 1
	}
	if s.cfg.Services.GitHub == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "GitHub repositories are not available"}
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodGitHubRepositoriesList); err != nil {
		return nil, err
	}
	page, err := s.cfg.Services.GitHub.Repositories(ctx, member, p.Page)
	if err != nil {
		return nil, rpcError(err)
	}
	result := protocol.GitHubRepositoryListResult{
		Account:      protocol.GitHubAccount{ID: page.Account.ID, Login: page.Account.Login},
		Repositories: make([]protocol.GitHubRepository, len(page.Repositories)),
		NextPage:     page.NextPage,
	}
	for i, repo := range page.Repositories {
		result.Repositories[i] = protocol.GitHubRepository{
			ID: repo.ID, FullName: repo.FullName, Name: repo.Name, Private: repo.Private,
			DefaultBranch: repo.DefaultBranch, CloneURL: repo.CloneURL, CanPush: repo.CanPush,
		}
	}
	return result, nil
}

// resolveGitHubMirror resolves read access and account identity before a
// workspace is created or its source is changed. A repository collaborator is
// sufficient; push permission is not needed for a read-only mirror.
func (s *Server) resolveGitHubMirror(ctx context.Context, member domain.MemberID, sourceURL, branch string, expectedID int64) (mirrorservice.ConfigureRequest, *protocol.Error) {
	source, err := mirrorservice.CanonicalizeSource(sourceURL, domain.MirrorAuthGitHub, "")
	if err != nil {
		return mirrorservice.ConfigureRequest{}, invalidParams(err.Error())
	}
	if expectedID < 0 {
		return mirrorservice.ConfigureRequest{}, invalidParams("github_account_id must be greater than zero")
	}
	if s.cfg.Services.GitHub == nil {
		return mirrorservice.ConfigureRequest{}, &protocol.Error{Code: protocol.CodeUnavailable, Message: "GitHub repositories are not available"}
	}
	fullName := strings.TrimSuffix(strings.TrimPrefix(source.URL, "https://github.com/"), ".git")
	account, repo, err := s.cfg.Services.GitHub.Repository(ctx, member, fullName)
	if err != nil {
		return mirrorservice.ConfigureRequest{}, rpcError(err)
	}
	if account.ID <= 0 || (expectedID != 0 && account.ID != expectedID) {
		return mirrorservice.ConfigureRequest{}, &protocol.Error{Code: protocol.CodeInvalidState, Message: "GitHub account changed; reconnect and select the repository again"}
	}
	if repo.ID <= 0 || !strings.EqualFold(repo.FullName, fullName) || repo.CloneURL != "https://github.com/"+repo.FullName+".git" {
		return mirrorservice.ConfigureRequest{}, &protocol.Error{Code: protocol.CodeInvalidState, Message: "GitHub repository identity changed; select the repository again"}
	}
	if repo.DefaultBranch == "" {
		return mirrorservice.ConfigureRequest{}, &protocol.Error{Code: protocol.CodeInvalidState, Message: "GitHub repository has no default branch; create an initial commit before importing"}
	}
	if branch == "" {
		branch = repo.DefaultBranch
	}
	return mirrorservice.ConfigureRequest{
		SourceURL: repo.CloneURL, Branch: branch, Auth: domain.MirrorAuthGitHub,
		GitHubMemberID: member, GitHubUserID: account.ID,
	}, nil
}
