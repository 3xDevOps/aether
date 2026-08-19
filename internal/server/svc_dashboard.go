package server

import (
	"github.com/3xDevOps/Aether/internal/dashboard"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// The web dashboard gateway (): HTTP and WebSocket onto the same
// control-channel handlers, event bus, and PTY host SSH clients use. It
// exists only when a dashboard port or a direct address is configured,
// and it hands the SSH side the token table, because the dashboard's
// bearer tokens are minted on the authenticated SSH channel.
func init() {
	registerService("dashboard", func(d Deps) (Service, error) {
		if d.Config.DashboardPort == 0 && d.Config.DashboardAddr == "" {
			return nil, nil
		}
		gw, err := dashboard.New(dashboard.Config{
			Port:    d.Config.DashboardPort,
			Addr:    d.Config.DashboardAddr,
			RPC:     sshd.NewBridge(d.SSH),
			Bus:     d.Bus,
			PTY:     d.PTY,
			Git:     d.Git,
			DataDir: d.DataDir,
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Dashboard = gw.Tokens()
		return gw, nil
	})
}
