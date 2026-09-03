package serverupdate

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Host carries the three things a self-update does to the machine it runs
// on: replace this process image, ask the service manager to restart the
// unit, and tell whether there is a service manager to ask.
//
// It is never defaulted. A Service built without one refuses to apply
// anything and reports why through server.update_status, so a test binary
// cannot reach the host's systemd by leaving a field nil - which is
// exactly what `make test-integration` did before: it injected Exec,
// left Restart to its default, and shelled out to
// `systemctl restart aether-server` on the developer's own box.
type Host struct {
	// Exec replaces this process image with the binary at path. It does
	// not return on success.
	Exec func(path string, argv, env []string) error
	// Restart asks the service manager to restart the unit. It is the
	// fallback for an Exec that failed.
	Restart func() error
	// UnderSystemd reports whether this process was started by a systemd
	// unit, which is the only case where Restart is worth trying.
	UnderSystemd func() bool
}

// complete reports whether every hook is set. A partially filled Host is
// treated as no Host at all rather than half-used.
func (h Host) complete() bool {
	return h.Exec != nil && h.Restart != nil && h.UnderSystemd != nil
}

// HostProcess returns the real controls: syscall.Exec, `systemctl restart
// aether-server`, and the INVOCATION_ID that systemd sets for every unit
// it starts.
//
// Only cmd/aether-server calls this. It is the single opt-in that lets a
// process act on the host it runs on, so grepping for it finds every place
// that can.
func HostProcess() Host {
	return Host{
		Exec:         syscall.Exec,
		Restart:      systemctlRestart,
		UnderSystemd: func() bool { return os.Getenv("INVOCATION_ID") != "" },
	}
}

// systemctlRestart is the fallback restart when re-exec fails under a
// systemd unit.
func systemctlRestart() error {
	out, err := exec.Command("systemctl", "restart", "aether-server").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart aether-server: %w: %s", err, out)
	}
	return nil
}
