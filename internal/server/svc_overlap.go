package server

import "github.com/3xDevOps/cloudfield/internal/overlap"

func init() {
	registerService("overlap", func(d Deps) (Service, error) {
		idx := overlap.NewIndex(d.Bus, d.Store, d.Events)
		d.SSH.Services.Overlaps = idx
		return idx, nil
	})
}
