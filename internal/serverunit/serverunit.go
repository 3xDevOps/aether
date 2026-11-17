// Package serverunit names the server's systemd unit. It is apart from
// serversetup so the CLI, which runs on every platform, can print restart
// lines without importing the server's setup code.
package serverunit

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Name is the server's unit, without the .service suffix.
const Name = "aether-server"

// Alias is the unit's second name, a symlink to its unit file. The unit
// keeps its name and text, because a binary from before the second name
// restarts it by that name after a rollback. systemctl enable refuses an
// alias, so enabling keeps Name.
const Alias = "cloudfield-server.service"

// AliasResolves reports whether systemd loads Alias as the server's unit,
// which takes the link and a systemd that has read it.
func AliasResolves() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "-p", "Id", "--value", Alias).Output()
	return err == nil && strings.TrimSpace(string(out)) == Name+".service"
}

// RestartName is the unit name to print in a restart line: Alias when
// systemd resolves it, else Name.
func RestartName() string {
	if AliasResolves() {
		return strings.TrimSuffix(Alias, ".service")
	}
	return Name
}
