package server

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/slack"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// The Slack bot, once an admin stored its tokens with `aether-server
// slack setup`. It hears needs from the push service, whose builder runs
// first because Go runs svc_push.go's init before this file's.
func init() {
	registerService("slack", func(d Deps) (Service, error) {
		pushSvc, ok := d.SSH.Services.Push.(*push.Service)
		if d.DataDir == "" || !ok {
			return nil, nil
		}
		app, bot, err := slack.ReadTokens(slack.Dir(d.DataDir))
		if errors.Is(err, slack.ErrNotSetUp) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		st, ok := d.Store.(slack.Store)
		if !ok {
			return nil, errors.New("slack: the store does not keep Slack links")
		}
		svc, err := slack.New(slack.Config{
			Store: st, AppToken: app, BotToken: bot, Name: d.SSH.Name,
			Local: func(ctx context.Context, grant sshd.ConnectorGrant, id string) (slack.Caller, error) {
				srv := d.SSHServer()
				if checkErr := srv.CheckConnectorGrant(ctx, grant); checkErr != nil {
					return nil, checkErr
				}
				return srv.ConnectorLocal(grant, id, slack.Client, slack.Scopes), nil
			},
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Slack = svc
		pushSvc.UseSink(svc)
		return svc, nil
	})
}
