package serverupdate

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
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
	// RestartsOnExit75 reports whether a systemd unit will start this
	// process again after it exits with status 75. When it does, a restart
	// for an update exits instead of re-executing, so systemd sees it. Nil
	// means no: the process re-executes in place.
	RestartsOnExit75 func() bool
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

		RestartsOnExit75: unitRestartsOnExit75,
	}
}

// unitRestartsOnExit75 asks systemd how the unit restarts. Any failure to
// find out reads as no, which keeps the in-place re-exec.
func unitRestartsOnExit75() bool {
	if os.Getenv("INVOCATION_ID") == "" {
		return false
	}
	out, err := exec.Command("systemctl", "show", "aether-server",
		"--property=Restart,RestartForceExitStatus,RestartPreventExitStatus,SuccessExitStatus").Output()
	return err == nil && restartsOnExit75(string(out))
}

// restartsOnExit75 reads `systemctl show` output. Exit status 75 is
// EX_TEMPFAIL. Restart=on-failure counts it as a failure unless
// SuccessExitStatus= lists it.
func restartsOnExit75(show string) bool {
	props := map[string][]string{}
	for line := range strings.SplitSeq(show, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = strings.Fields(v)
		}
	}
	lists75 := func(k string) bool {
		return slices.Contains(props[k], "75") || slices.Contains(props[k], "TEMPFAIL")
	}
	if lists75("RestartPreventExitStatus") {
		return false
	}
	return slices.Equal(props["Restart"], []string{"always"}) || lists75("RestartForceExitStatus") ||
		slices.Equal(props["Restart"], []string{"on-failure"}) && !lists75("SuccessExitStatus")
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
