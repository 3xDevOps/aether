//go:build integration

package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/3xDevOps/Aether/internal/domain"
)

// printAccess is a "claude" argv that prints the access token of the login
// it finds, as the agent would send it.
var printAccess = []string{"sh", "-c", `sleep 1; printf 'harness-login:%s\n' "$(sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p' "$HOME/.claude/.credentials.json")"; sleep 1`}

// TestIntegrationSessionLoginDocker launches Ada's run on Grace's listed
// account for real: the agent reads Grace's access token from the run's own
// copy, which the container sees by inode and cannot write, and which
// follows Grace's login in place. Ada's own login under it is left alone.
func TestIntegrationSessionLoginDocker(t *testing.T) {
	e, docker, cli, owner := newShareDockerEnv(t, printAccess)
	ctx := t.Context()
	if _, err := e.db.ShareAccount(ctx, owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.cfg.SessionLoginAccounts = map[domain.MemberID]bool{owner.ID: true}
	ownerHome, err := e.cfg.Homes.Path(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	adaHome, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := time.Now().Add(8 * time.Hour)
	adaLogin := `{"claudeAiOauth":{"accessToken":"ada-access","refreshToken":"ada~1"}}`
	seedHome(t, ownerHome, map[string]string{claudeLogin: claudeOwnerLogin("grace-access-1", "grace~1", in)})
	seedHome(t, adaHome, map[string]string{claudeLogin: adaLogin})
	adaOwn := filepath.Join(adaHome, filepath.FromSlash(claudeLogin))

	shared := launchShareRun(t, e, docker, cli, e.member.ID, owner.ID, "session login", "grace-access-1")
	copyFile := e.sched.sessionLoginPath(shared.run.ID, "claude")
	const target = "/root/.claude/.credentials.json"
	shared.want(t, "stat -c %i "+target, fmt.Sprintf("%d\n", inodeOf(t, copyFile)))
	if code, out := shared.sh(t, "grep -e refreshToken -e mcp-secret "+target); code != 1 {
		t.Fatalf("grep for the refresh token in the session's login exited %d: %s", code, out)
	}
	if code, out := shared.sh(t, "printf ada-login > "+target); code == 0 {
		t.Fatalf("the session wrote its login copy: %s", out)
	}
	info, err := cli.ContainerInspect(ctx, string(shared.id), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Container.Config.Labels["aether.inode-checks"]; got != `["`+target+`"]` {
		t.Fatalf("inode-check label = %q, want the login target", got)
	}

	seedHome(t, ownerHome, map[string]string{claudeLogin: claudeOwnerLogin("grace-access-2", "grace~2", in)})
	e.sched.syncSessionLogins(ctx)
	shared.want(t, `sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p' `+target, "grace-access-2")
	shared.want(t, "stat -c %i "+target, fmt.Sprintf("%d\n", inodeOf(t, copyFile)))
	if got := readFile(t, adaOwn); got != adaLogin {
		t.Fatalf("Ada's own login = %s, want it untouched", got)
	}

	// Ada's own containers pin her login, so none of them can rename over
	// the mountpoint the copy is bound on.
	own := launchShareRun(t, e, docker, cli, e.member.ID, e.member.ID, "own", "ada-access")
	if code, out := own.sh(t, "printf x > "+target+".tmp && mv -f "+target+".tmp "+target); code == 0 || !strings.Contains(strings.ToLower(out), "busy") {
		t.Fatalf("rename over Ada's pinned login = exit %d, %q; want it refused busy", code, out)
	}
	shared.want(t, `sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p' `+target, "grace-access-2")
	own.close(t, e)

	// A rename on the host detaches the bind, as it does a subpath mount's,
	// and the check every start makes refuses the container.
	tmp := adaOwn + ".new"
	if err := os.WriteFile(tmp, []byte(adaLogin), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, adaOwn); err != nil {
		t.Fatal(err)
	}
	if err := docker.Start(ctx, shared.id); err == nil || !strings.Contains(err.Error(), target+" is not a mount point in the container") {
		t.Fatalf("Start after the copy was detached = %v, want it refused", err)
	}
	shared.close(t, e)
	if _, err := os.Stat(filepath.Dir(copyFile)); !os.IsNotExist(err) {
		t.Fatalf("session login directory after close = %v, want removed", err)
	}
}
