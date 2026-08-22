package server

import (
	"github.com/3xDevOps/Aether/internal/disk"
)

// The gateway read seams: run.patch and server.disk are control-channel
// methods with no background service of their own, so this builder only
// attaches what they read - the git engine's patch renderer and a cached
// disk gauge over the data directory - and returns no Service.
func init() {
	registerService("gateway", func(d Deps) (Service, error) {
		if d.Git != nil {
			d.SSH.Services.Patch = d.Git
		}
		if d.DataDir != "" {
			d.SSH.Services.Disk = disk.NewCache(d.DataDir, 0)
		}
		return nil, nil
	})
}
