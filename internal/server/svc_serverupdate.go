package server

import (
	"log/slog"

	"github.com/3xDevOps/Aether/internal/serverupdate"
)

// Server self-update (): the admin-triggered swap of the server's own
// binaries. The service answers the two control methods and rides the
// scheduler's poll loop, which is what makes a scheduled update land at
// the first idle moment; it owns no goroutine of its own.
//
// Config.SelfUpdate.Host is what lets it act on the host, and only
// cmd/aether-server fills it in. An embedded server - the end-to-end
// suite, anything importing this package - gets a service that reports
// itself incapable rather than one that can reach the machine's systemd.
func init() {
	registerService("serverupdate", func(d Deps) (Service, error) {
		svc, err := serverupdate.New(serverupdate.Config{
			Store:      d.Store,
			Bus:        d.Bus,
			Busy:       d.Runs.Busy,
			Checker:    d.Config.SelfUpdate.Checker,
			Host:       d.Config.SelfUpdate.Host,
			Executable: d.Config.SelfUpdate.Executable,
			Now:        d.Config.SelfUpdate.Now,
		})
		if err != nil {
			// Self-update is a convenience; refusing to boot over it would
			// trade a working deployment for a missing button. The
			// handlers answer -32004 for an unset seam.
			slog.Error("server: server self-update is unavailable", "error", err)
			return nil, nil
		}
		d.SSH.Services.ServerUpdate = svc
		d.Runs.UseUpdates(svc)
		return nil, nil
	})
}
