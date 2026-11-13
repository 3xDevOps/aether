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

func (d *Docker) prepareUser(ctx context.Context, id ID, user, home string, sudo bool) error {
	allow := ""
	if sudo {
		allow = "sudo"
	}
	code, output, _, err := d.execCombined(ctx, id, []string{"/bin/sh", "-c", UserScript, "aether-user", user, cmp.Or(home, "/"), allow}, "", "0:0")
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
