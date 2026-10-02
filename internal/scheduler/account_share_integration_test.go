//go:build integration

package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func seedHome(t *testing.T, home string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		name := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// shareRun is one live run container seen through the engine.
type shareRun struct {
	run    *domain.Run
	docker *runtime.Docker
	id     runtime.ID
	mounts []container.MountPoint
}

// launchShareRun launches harness "claude" for member on account and
// returns its container once the harness has printed its login and exited
// into the supervisor's login shell. The container and every named volume
// it mounts are removed when the test ends.
func launchShareRun(t *testing.T, e *testEnv, docker *runtime.Docker, cli *client.Client, member, account domain.MemberID, task, wantLogin string) shareRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	run, err := e.sched.Launch(ctx, e.ws.ID, member, account, task, "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch(%s on %s): %v", member, account, err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("readSidecar: %v", err)
	}
	id := runtime.ID(sc.ContainerID)
	info, err := cli.ContainerInspect(ctx, sc.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect run container: %v", err)
	}
	var volumes []string
	for _, m := range info.Container.Mounts {
		if m.Type == mount.TypeVolume {
			volumes = append(volumes, m.Name)
		}
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = docker.Destroy(cctx, id)
		for _, v := range volumes {
			if _, err := cli.VolumeRemove(cctx, v, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
				t.Errorf("cleanup VolumeRemove(%s) error: %v", v, err)
			}
		}
	})
	if run.Status != domain.RunRunning {
		t.Fatalf("run status after launch = %s, want running", run.Status)
	}
	sess := e.pty.session(run.ID)
	if sess == nil {
		t.Fatal("no pty session recorded")
	}
	waitFor(t, "harness exit", func() bool {
		return strings.Contains(sess.output(), "[aether] harness exited with code 0")
	})
	if out := sess.output(); !strings.Contains(out, "harness-login:"+wantLogin+"\r") && !strings.Contains(out, "harness-login:"+wantLogin+"\n") {
		t.Fatalf("harness did not print login %q; pty output = %q", wantLogin, out)
	}
	return shareRun{run: run, docker: docker, id: id, mounts: info.Container.Mounts}
}

func (r shareRun) sh(t *testing.T, script string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	code, stdout, stderr, err := r.docker.Exec(ctx, r.id, []string{"/bin/sh", "-c", script}, "")
	if err != nil {
		t.Fatalf("exec %q error: %v (stdout=%q stderr=%q)", script, err, stdout, stderr)
	}
	return code, stdout + stderr
}

func (r shareRun) want(t *testing.T, script, want string) {
	t.Helper()
	code, out := r.sh(t, script)
	if code != 0 || out != want {
		t.Fatalf("%q = exit %d, %q; want %q", script, code, out, want)
	}
}

// engineMounts lists the engine's view of the container's volume mounts,
// and its bind mounts whose source lies in home, as "type source at
// destination".
func (r shareRun) engineMounts(home string) (volumes, binds []string) {
	for _, m := range r.mounts {
		desc := string(m.Type) + " " + m.Name + m.Source + " at " + m.Destination
		switch {
		case m.Type == mount.TypeVolume:
			volumes = append(volumes, desc)
		case m.Source == home || strings.HasPrefix(m.Source, home+"/"):
			binds = append(binds, desc)
		}
	}
	return volumes, binds
}

// ownerMounts lists the container's mount table entries whose root lies in
// home, as "root at point".
func (r shareRun) ownerMounts(t *testing.T, home string) []string {
	t.Helper()
	code, table := r.sh(t, "cat /proc/self/mountinfo")
	if code != 0 {
		t.Fatalf("read mountinfo: exit %d, %q", code, table)
	}
	marker := "/homes/" + filepath.Base(home) + "/"
	var out []string
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && strings.Contains(f[3]+"/", marker) {
			out = append(out, f[3]+" at "+f[4])
		}
	}
	return out
}

// close closes the run merged and waits for its container to be destroyed.
func (r shareRun) close(t *testing.T, e *testEnv) {
	t.Helper()
	if err := e.sched.CloseRun(t.Context(), r.run.ID, r.run.MemberID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.waitStoreStatus(t, r.run.ID, domain.RunMerged)
	waitFor(t, "sidecar removed", func() bool {
		_, err := os.Stat(e.sched.sidecarPath(r.run.ID))
		return os.IsNotExist(err)
	})
}

// TestIntegrationSharedAccountRunDocker launches a real run for Ada on
// Grace's shared account: the container is Ada's image and home with only
// Grace's Claude login mounted in, and Ada's identity authors its commit.
// Ada's later run on her own account mounts nothing of Grace's.
func TestIntegrationSharedAccountRunDocker(t *testing.T) {
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = docker
		cfg.RunContainerTTL = -time.Second
		cfg.Harnesses = map[string]HarnessSpec{
			// An argv override keeps the shipped claude profile, whose login
			// path is .claude/.credentials.json. The leading sleep keeps the
			// output behind the attach.
			"claude": {TUIArgs: []string{"sh", "-c",
				`sleep 1; printf 'harness-login:%s\n' "$(cat "$HOME/.claude/.credentials.json")"; sleep 1`}},
		}
	})
	ctx := t.Context()
	owner := &domain.Member{DisplayName: "Grace", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err = e.db.CreateMember(ctx, owner); err != nil {
		t.Fatal(err)
	}
	// A run on Grace's account that used her image would fail here: it
	// does not exist in the engine.
	if err = e.db.UpdateMemberImage(ctx, owner.ID, "aether/grace-absent:1"); err != nil {
		t.Fatal(err)
	}
	ownerHome, err := e.cfg.Homes.Path(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	adaHome, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	seedHome(t, ownerHome, map[string]string{
		".claude/.credentials.json": "grace-login",
		".gitconfig":                "[user]\n\temail = grace-config@example.com\n",
		".config/gh/hosts.yml":      "grace-gh-token",
		".ssh/id_ed25519":           "grace-ssh-key",
	})
	const adaGitconfig = "[user]\n\temail = ada-config@example.com\n"
	seedHome(t, adaHome, map[string]string{
		".gitconfig":           adaGitconfig,
		".config/gh/hosts.yml": "ada-gh-token",
	})
	ada := e.member.GitIdentity()

	shared := launchShareRun(t, e, docker, cli, e.member.ID, owner.ID, "shared smoke", "grace-login")
	shared.want(t, "cat /root/.claude/.credentials.json", "grace-login")
	shared.want(t, "cat /root/.gitconfig", adaGitconfig)
	shared.want(t, "cat /root/.config/gh/hosts.yml", "ada-gh-token")
	shared.want(t, `printf '%s|%s' "$GIT_AUTHOR_EMAIL" "$GIT_COMMITTER_EMAIL"`, ada.Email+"|"+ada.Email)
	if code, out := shared.sh(t, `[ -e /root/.ssh ] || [ -L /root/.ssh ]`); code == 0 {
		t.Fatalf("/root/.ssh exists in the shared run: %s", out)
	}
	if code, out := shared.sh(t, "grep -r -l -e grace-gh-token -e grace-ssh-key -e grace-config@example.com /root"); code != 1 {
		t.Fatalf("grep for Grace's GitHub, SSH and git files under /root exited %d: %s", code, out)
	}
	volumes, binds := shared.engineMounts(ownerHome)
	t.Logf("engine mounts: volumes %v, binds from Grace's home %v", volumes, binds)
	if len(volumes) != 1 || !strings.HasSuffix(volumes[0], " at /root/.claude/.credentials.json") || len(binds) > 0 {
		t.Fatalf("shared run engine mounts: volumes %v, binds from Grace's home %v; want only the login volume at /root/.claude/.credentials.json", volumes, binds)
	}
	for _, m := range shared.ownerMounts(t, ownerHome) {
		t.Logf("mountinfo: %s", m)
		if !strings.HasSuffix(m, " at /root/.claude/.credentials.json") {
			t.Fatalf("Grace's home is mounted other than at her login: %s", m)
		}
	}
	shared.close(t, e)
	authors := e.git.commitAuthors(shared.run.ID)
	if len(authors) == 0 {
		t.Fatal("shared run closed without a commit")
	}
	for _, a := range authors {
		if a != ada {
			t.Fatalf("shared run commit author = %v, want the launcher %v", a, ada)
		}
	}

	own := launchShareRun(t, e, docker, cli, e.member.ID, e.member.ID, "own smoke", "")
	if volumes, binds := own.engineMounts(ownerHome); len(volumes) > 0 || len(binds) > 0 {
		t.Fatalf("Ada's own run engine mounts: volumes %v, binds from Grace's home %v; want neither", volumes, binds)
	}
	if mounts := own.ownerMounts(t, ownerHome); len(mounts) > 0 {
		t.Fatalf("Ada's own run mounts Grace's home: %v", mounts)
	}
	own.want(t, "cat /root/.claude/.credentials.json", "")
	own.want(t, "cat /root/.gitconfig", adaGitconfig)
	own.close(t, e)
}
