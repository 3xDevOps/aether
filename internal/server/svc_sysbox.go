package server

import "github.com/3xDevOps/Aether/internal/sysbox"

// The admin-triggered Sysbox install. Once it ends the Docker runtime asks
// the daemon again, so the next container follows what the install left.
func init() {
	registerService("sysbox", func(d Deps) (Service, error) {
		if !d.Config.SysboxInstaller {
			return nil, nil
		}
		m := sysbox.NewManager()
		if docker, ok := d.Runtime.(interface {
			ForgetRuntime()
			VetoSysbox(func() bool)
		}); ok {
			m.Recheck = docker.ForgetRuntime
			docker.VetoSysbox(m.Unusable)
		}
		d.SSH.Services.Sysbox = m
		return m, nil
	})
}
