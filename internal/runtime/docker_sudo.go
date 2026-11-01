package runtime

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/moby/moby/api/pkg/stdcopy"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/devexec"
)

// userScript gives uid $1 a passwd entry whose home is $2: git refuses to
// commit and ssh and ssh-keygen refuse to run for a uid with none, and ssh
// reads ~/.ssh from the passwd home, never $HOME. With $3 set it also lets
// the uid sudo without a password; the sudoers file is checked before it
// goes live, since sudo refuses to run at all with a broken one.
const userScript = `uid=${1%%:*} gid=${1#*:}
if grep -q "^[^:]*:[^:]*:$uid:" /etc/passwd; then
	awk -F: -v OFS=: -v u="$uid" -v h="$2" '$3 == u { $6 = h } { print }' /etc/passwd > /etc/passwd.aether && cat /etc/passwd.aether > /etc/passwd && rm -f /etc/passwd.aether || exit 1
else
	echo "aether:x:$uid:$gid::$2:/bin/sh" >> /etc/passwd || exit 1
fi
grep -q "^[^:]*:[^:]*:$gid:" /etc/group || echo "aether:x:$gid:" >> /etc/group || exit 1
[ -n "$3" ] || exit 0
mkdir -p /etc/sudoers.d
printf '#%s ALL=(ALL:ALL) NOPASSWD: ALL\n' "$uid" > /etc/sudoers.d/.aether.new
chmod 0440 /etc/sudoers.d/.aether.new
if command -v visudo >/dev/null 2>&1 && ! visudo -cqf /etc/sudoers.d/.aether.new; then rm -f /etc/sudoers.d/.aether.new; exit 1; fi
mv -f /etc/sudoers.d/.aether.new /etc/sudoers.d/aether`

func unprivileged(user string) bool {
	uid, _, _ := strings.Cut(user, ":")
	return uid != "" && uid != "0"
}

func (d *Docker) prepareUser(ctx context.Context, id ID, user, home string, sudo bool) error {
	allow := ""
	if sudo {
		allow = "sudo"
	}
	code, output, _, err := d.execCombined(ctx, id, []string{"/bin/sh", "-c", userScript, "aether-user", user, cmp.Or(home, "/"), allow}, "", "0:0")
	if err != nil {
		return fmt.Errorf("runtime: prepare user %s: %w", user, err)
	}
	if code != 0 {
		return fmt.Errorf("runtime: prepare user %s exited %d: %s", user, code, strings.TrimSpace(output))
	}
	return nil
}

var _ SudoRuntime = (*Docker)(nil)

func (d *Docker) SettingUp(id ID) bool {
	_, ok := d.settingUp.Load(id)
	return ok
}

// ExecAttached runs the staged helper, whose input stays open for as long as
// exec holds the attachment: closing it, when ctx ends or the output cannot
// be delivered, is what kills the command.
func (d *Docker) ExecAttached(ctx context.Context, id ID, spec AttachedExec) (int, error) {
	cmd := append([]string{coordtransport.CLIPath, devexec.Command, devexec.Attached, strconv.Itoa(len(spec.Stdin))}, spec.Argv...)
	stdin := spec.Stdin
	if stdin == nil {
		stdin = []byte{}
	}
	return d.exec(ctx, id, cmd, spec.WorkingDir, spec.User, stdin, func(r io.Reader) error {
		_, err := stdcopy.StdCopy(spec.Stdout, spec.Stderr, r)
		return err
	})
}

func envValue(env []string, name string) string {
	for _, pair := range env {
		if key, value, _ := strings.Cut(pair, "="); key == name {
			return value
		}
	}
	return ""
}
