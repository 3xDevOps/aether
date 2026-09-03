package server

import (
	"github.com/3xDevOps/Aether/internal/serverupdate"
)

// Server self-update (): the admin-triggered swap of the server's own
// binaries. The service answers the two control methods and rides the
// scheduler's poll loop, which is what makes a scheduled update land at
// the first idle moment; it owns no goroutine of its own.
func init() {
	registerService("serverupdate", func(d Deps) (Service, error) {
		svc, err := serverupdate.New(serverupdate.Config{
			Store:      d.Store,
			Bus:        d.Bus,
			Checker:    d.Config.SelfUpdate.Checker,
			Executable: d.Config.SelfUpdate.Executable,
			Exec:       d.Config.SelfUpdate.Exec,
			Restart:    d.Config.SelfUpdate.Restart,
			Now:        d.Config.SelfUpdate.Now,
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.ServerUpdate = svc
		d.Runs.UseUpdates(svc)
		return nil, nil
	})
}
