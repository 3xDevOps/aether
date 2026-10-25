package server

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
)

// Web Push notifications: a bus consumer that tells a run's owner, or a
// swarm's accountable human, on the devices they subscribed, that the run
// started needing them, and admins that a workspace's budget rose.
// Subscriptions are tied to the key, so it lives with the server's other
// durable state. It reads budgets from the cost service, whose builder runs
// first because Go runs svc_cost.go's init before this file's.
func init() {
	registerService("push", func(d Deps) (Service, error) {
		if d.DataDir == "" || d.Runs == nil {
			return nil, nil
		}
		st, ok := d.Store.(push.Store)
		if !ok {
			return nil, errors.New("push: the store does not list a swarm's runs")
		}
		svc, err := push.New(push.Config{
			Store: st,
			Bus:   d.Bus,
			Runs:  d.Runs,
			Swarm: func(ctx context.Context, run *domain.Run) (*protocol.AttentionItem, domain.MemberID, error) {
				return d.SSHServer().SwarmAttention(ctx, run)
			},
			Budgets: d.SSH.Services.Costs,
			KeyPath: filepath.Join(d.DataDir, "push", "vapid_key.pem"),
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Push = svc
		if d.Assistant != nil {
			d.Assistant.UseNotifier(svc.Notify)
		}
		return svc, nil
	})
}
