package server

import (
	"errors"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/push"
)

// Web Push notifications: a bus consumer that tells a run's owner on the devices they subscribed, that the run
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
			return nil, errors.New("push: the store does not keep push subscriptions")
		}
		svc, err := push.New(push.Config{
			Store:   st,
			Bus:     d.Bus,
			Runs:    d.Runs,
			Budgets: d.SSH.Services.Costs,
			KeyPath: filepath.Join(d.DataDir, "push", "vapid_key.pem"),
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Push = svc
		return svc, nil
	})
}
