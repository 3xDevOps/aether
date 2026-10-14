package protocol

// Sysbox methods, both admin only. Sysbox is the container runtime that lets
// runs start their own Docker daemon.
const (
	// MethodServerSysboxStatus reports whether the Docker daemon has Sysbox
	// and how the last install went.
	MethodServerSysboxStatus = "server.sysbox.status"
	// MethodServerSysboxInstall starts the Sysbox installer in the
	// background and returns the status. A second call while it runs
	// changes nothing.
	MethodServerSysboxInstall = "server.sysbox.install"
)

// State values of SysboxStatus.
const (
	SysboxOn          = "on"
	SysboxOff         = "off"
	SysboxUnsupported = "unsupported"
	SysboxInstalling  = "installing"
	SysboxFailed      = "failed"
)

// SysboxStatus is the result of both Sysbox methods. Reason says why the
// state is off, unsupported or failed; Log is the last 8 KiB of installer
// output.
type SysboxStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	Log    string `json:"log"`
}
