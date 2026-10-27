package server

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/children"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// Child runs attach to the scheduler, the git engine and the launch
// admission every run launch uses. The coordination builder, which runs
// after this one, hands the service its mailbox.
func init() {
	registerService("children", func(d Deps) (Service, error) {
		st, ok := d.Store.(children.Store)
		if !ok {
			return nil, errors.New("children: store does not implement the child run store")
		}
		if d.Runs == nil || d.Git == nil {
			return nil, nil
		}
		svc, err := children.New(children.Config{
			Store: st, Runs: d.Runs, Git: d.Git, Usage: d.SSH.Services.Usage, Bus: d.Bus, Recovered: d.Runs.RecoveryReady(),
			Agents: func() children.Agents {
				if d.SSHServer == nil {
					return nil
				}
				if srv := d.SSHServer(); srv != nil {
					return srv
				}
				return nil
			},
			Cost: func() children.Admitter {
				if d.SSH.Services.Costs == nil {
					return nil
				}
				return d.SSH.Services.Costs
			},
			Authorize: func(ctx context.Context, st store.Store, actor domain.MemberID, account string) (domain.MemberID, error) {
				admission, err := sshd.AuthorizeLaunch(ctx, st, actor, account)
				if err != nil {
					return "", err
				}
				return admission.Account.ID, nil
			},
			AuthorizationMu: d.SSH.AuthorizationMu,
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Children = svc
		return svc, nil
	})
}
