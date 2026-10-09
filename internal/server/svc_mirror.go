package server

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/github"
	mirrorsvc "github.com/3xDevOps/Aether/internal/mirror"
)

// Workspace mirrors are synchronous: the builder attaches one service
// instance to both the control-channel handlers and launch base capture.
func init() {
	registerService("mirror", func(d Deps) (Service, error) {
		githubService := github.New(d.SSH.Homes)
		d.SSH.Services.GitHub = githubService
		d.SSH.Services.GitHubOAuth = d.Runs
		svc, err := mirrorsvc.New(mirrorsvc.Config{
			Root:  filepath.Join(d.DataDir, "mirrors"),
			Store: d.Store,
			Git:   d.Git,
			GitHubCredentials: func(ctx context.Context, memberID domain.MemberID) (string, int64, error) {
				member, err := d.Store.GetMember(ctx, memberID)
				if err != nil || member == nil || member.Pending {
					return "", 0, errors.New("GitHub authorizing member is unavailable")
				}
				token, account, err := githubService.Credentials(ctx, memberID)
				return token, account.ID, err
			},
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Mirrors = svc
		d.Workspaces.mirrors = svc
		d.Runs.UseBaseCapture(svc)
		return nil, nil
	})
}
