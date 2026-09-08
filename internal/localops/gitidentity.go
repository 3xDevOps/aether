package localops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GitIdentity reads this machine's own `user.name` and `user.email`, the
// pair the user's local commits already carry, so the onboarding wizard
// can offer it instead of asking the user to retype it.
//
// An unset key is an empty value, not a failure: git exits 1 with no
// output when it cannot resolve one. Only a git that cannot run at all,
// or one that failed for another reason, is an error.
func GitIdentity(ctx context.Context) (string, string, error) {
	name, err := gitConfigValue(ctx, "user.name")
	if err != nil {
		return "", "", err
	}
	email, err := gitConfigValue(ctx, "user.email")
	if err != nil {
		return "", "", err
	}
	return name, email, nil
}

// gitConfigValue reads one git config key from wherever git resolves it -
// system, global, or the current directory's repository.
func gitConfigValue(ctx context.Context, key string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", "--get", key)
	// Reading a config key cannot need a credential, but a prompt would
	// hold the gateway request open until the timeout fired.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("git config --get %s: %w: %s", key, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
