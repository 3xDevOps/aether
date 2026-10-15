// Package scripts embeds the host scripts the aether-server binary runs.
package scripts

import _ "embed"

// InstallSysbox is install-sysbox.sh, the pinned Sysbox installer.
//
//go:embed install-sysbox.sh
var InstallSysbox string
