package server

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/watch"
)

// Watches: a bus consumer that evaluates each member's watches on runs and
// swarms and delivers their notices in-app and through Web Push. It reads
// the push service svc_push.go builds, which Go initializes first.
func init() {
	registerService("watch", func(d Deps) (Service, error) {
		if d.Runs == nil {
			return nil, nil
		}
		st, ok := d.Store.(watch.Store)
		if !ok {
			return nil, errors.New("watch: the store does not keep watches")
		}
		cfg := watch.Config{
			Store: st,
			Bus:   d.Bus,
			Runs:  d.Runs,
			Swarm: func(ctx context.Context, run *domain.Run) (*protocol.AttentionItem, domain.MemberID, error) {
				return d.SSHServer().SwarmAttention(ctx, run)
			},
		}
		if p, ok := d.SSH.Services.Push.(*push.Service); ok {
			cfg.Push = p
		}
		if d.Events != nil {
			cfg.Head = d.Events.LastSeq
		}
		svc, err := watch.New(cfg)
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Watches = svc
		return svc, nil
	})
}
