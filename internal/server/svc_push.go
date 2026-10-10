package server

import (
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/push"
)

// Web Push notifications: a bus consumer that tells a run's owner, on the
// devices they subscribed, that the run started needing them. Subscriptions
// are tied to the key, so it lives with the server's other durable state.
func init() {
	registerService("push", func(d Deps) (Service, error) {
		if d.DataDir == "" || d.Runs == nil {
			return nil, nil
		}
		svc, err := push.New(push.Config{
			Store:   d.Store,
			Bus:     d.Bus,
			Runs:    d.Runs,
			KeyPath: filepath.Join(d.DataDir, "push", "vapid_key.pem"),
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Push = svc
		return svc, nil
	})
}
