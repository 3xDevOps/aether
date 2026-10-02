package server

import "github.com/3xDevOps/Aether/internal/quota"

func init() {
	registerService("usage", func(d Deps) (Service, error) {
		d.SSH.Services.Usage = quota.New(d.SSH.Homes)
		return nil, nil
	})
}
