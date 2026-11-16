package serversetup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// OMPBroker is the oh-my-pi release whose auth broker holds and refreshes
// the omp logins of session VMs. scripts/bump-agent-pins.sh moves it to the
// newest release, which members' own omp installs follow.
var OMPBroker = Pinned{
	Name: "oh-my-pi 18.20.3",
	URL:  "https://github.com/can1357/oh-my-pi/releases/download/v18.20.3/omp-linux-x64",
	Path: "/usr/local/libexec/aether/omp",
	Mode: 0o755,
	Asset: selfupdate.Asset{
		SHA256: "f34ec9b0f0c368f1937701e2fe111b3c29a7cf53314a642448b3c6cf2925d85a",
		Size:   288938160,
	},
}

// OMPBrokerUser is the system user omp's auth brokers run as: no login, no
// home and no group but its own.
const OMPBrokerUser = "aether-omp"

// EnsureSystemUser returns name's uid and gid, adding name with useradd as
// a system user with a group of its own when it is missing.
func EnsureSystemUser(ctx context.Context, name string) (int, int, error) {
	u, err := user.Lookup(name)
	if errors.As(err, new(user.UnknownUserError)) {
		out, runErr := exec.CommandContext(ctx, "useradd", "--system", "--user-group", "--no-create-home",
			"--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", name).CombinedOutput()
		if runErr != nil {
			return 0, 0, fmt.Errorf("useradd %s: %w: %s", name, runErr, strings.TrimSpace(string(out)))
		}
		u, err = user.Lookup(name)
	}
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, fmt.Errorf("user %s: uid %q: %w", name, u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("user %s: gid %q: %w", name, u.Gid, err)
	}
	return uid, gid, nil
}
