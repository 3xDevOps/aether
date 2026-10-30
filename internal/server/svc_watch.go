package server

import (
	"errors"

	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/watch"
)

// Watches: a bus consumer that evaluates each member's watches on runs and
// pull requests and delivers their notices in-app, through Web Push and, for the
// watches the assistant keeps on the runs it launches, to the assistant. It reads
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
			Store:     st,
			Bus:       d.Bus,
			Runs:      d.Runs,
			Recovered: d.Runs.RecoveryReady(),
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
		if d.Assistant != nil {
			svc.UseAssistant(d.Assistant)
			d.Assistant.UseWatches(svc)
		}
		return svc, nil
	})
}
