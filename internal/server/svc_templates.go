package server

import (
	"context"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/templates"
)

// Task templates and their cron schedules (). The cron loop fires
// templates unattended, which is why it re-checks the schedule creator's
// launch capability on every fire and launches through the same guarded
// controller the RPC handlers use. Base branch ages come straight from the
// workspace bare repos the git engine keeps under <data>/repos.
func init() {
	registerService("templates", func(d Deps) (Service, error) {
		svc, err := templates.New(templates.Config{
			Store: d.Store,
			Bus:   d.Bus,
			Runs:  guardedRuns{ssh: d.SSH},
			Base:  templates.RepoBase{Dir: filepath.Join(d.DataDir, "repos")},
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Templates = svc
		return svc, nil
	})
}

// guardedRuns launches through the same controller the RPC handlers use,
// read at launch time rather than at build time so a template launch or a
// cron fire goes through every gate another service decorated it with -
// the session budget () above all - no matter which service was
// built first.
type guardedRuns struct{ ssh *sshd.Config }

func (g guardedRuns) Launch(ctx context.Context, session domain.SessionID, member domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	return g.ssh.Runs.Launch(ctx, session, member, task, harness, mode)
}
