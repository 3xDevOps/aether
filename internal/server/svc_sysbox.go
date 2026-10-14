package server

import "github.com/3xDevOps/Aether/internal/sysbox"

// The admin-triggered Sysbox install. After it succeeds the Docker runtime
// forgets that the daemon lacked Sysbox, so the next container uses it.
func init() {
	registerService("sysbox", func(d Deps) (Service, error) {
		if !d.Config.SysboxInstaller {
			return nil, nil
		}
		m := sysbox.NewManager()
		if docker, ok := d.Runtime.(interface{ ForgetRuntime() }); ok {
			m.Recheck = docker.ForgetRuntime
		}
		d.SSH.Services.Sysbox = m
		return m, nil
	})
}
