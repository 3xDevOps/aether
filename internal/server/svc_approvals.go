package server

import (
	"github.com/3xDevOps/Aether/internal/approvals"
)

// The approval inbox and presence roster (): a bus consumer that
// turns adapter pause records into inbox requests and tracks who is
// online and watching.
func init() {
	registerService("approvals", func(d Deps) (Service, error) {
		svc, err := approvals.New(approvals.Config{Store: d.Store, Bus: d.Bus})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Approvals = svc
		return svc, nil
	})
}
