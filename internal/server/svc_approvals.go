package server

import (
	"github.com/3xDevOps/Aether/internal/approvals"
)

// The presence roster: a bus consumer that tracks who is online and
// watching.
func init() {
	registerService("approvals", func(d Deps) (Service, error) {
		svc, err := approvals.New(approvals.Config{Bus: d.Bus})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Approvals = svc
		return svc, nil
	})
}
