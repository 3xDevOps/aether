package server

import (
	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// Cost attribution and workspace budgets (): a bus consumer that
// records what each run spent - or records that nobody measured it - and
// gates new-run admission on the workspace's cap.
func init() {
	registerService("cost", func(d Deps) (Service, error) {
		svc, err := cost.New(cost.Config{Store: d.Store, Bus: d.Bus})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Costs = svc
		d.SSH.Runs = sshd.GuardRuns(d.SSH.Runs, svc, d.Store)
		return svc, nil
	})
}
