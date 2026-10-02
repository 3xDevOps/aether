//go:build integration && linux

package runtime

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
)

const (
	loginTarget = "/root/.claude/.credentials.json"
	loginRel    = ".claude/.credentials.json"
)

// subpathHomes is a launcher's home, an account owner's home, and a victim
// directory beside them, under one temporary directory. The fixed directory
// names let a container's mount table be matched against them.
type subpathHomes struct {
	launcher, owner, victim string
}

func newSubpathHomes(t *testing.T) subpathHomes {
	t.Helper()
	root := t.TempDir()
	h := subpathHomes{
		launcher: filepath.Join(root, "aether-launcher-home"),
		owner:    filepath.Join(root, "aether-owner-home"),
		victim:   filepath.Join(root, "aether-victim"),
	}
	for _, dir := range []string{h.launcher, h.owner, h.victim} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h subpathHomes) home() Mount {
	return Mount{HostPath: h.launcher, ContainerPath: "/root"}
}

func (h subpathHomes) login() Mount {
	return Mount{HostPath: h.owner, Subpath: loginRel, ContainerPath: loginTarget}
}

func writeTree(t *testing.T, base string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		name := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func plantLink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func readHost(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read host file: %v", err)
	}
	return string(data)
}

func inode(t *testing.T, name string) uint64 {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

// newSubpathDocker is newTestDocker on an engine that supports volume
// subpaths (API 1.45, Docker Engine 26.0).
func newSubpathDocker(t *testing.T) *Docker {
	t.Helper()
	d := newTestDocker(t)
	v, err := d.cli.ServerVersion(t.Context(), client.ServerVersionOptions{})
	if err != nil {
		t.Fatalf("ServerVersion() error: %v", err)
	}
	t.Logf("docker engine %s, API %s, client API %s", v.Version, v.APIVersion, d.cli.ClientVersion())
	if versions.LessThan(v.APIVersion, "1.45") {
		t.Fatalf("docker engine %s serves API %s; volume subpath mounts need API 1.45 (Engine 26.0)", v.Version, v.APIVersion)
	}
	return d
}

// subpathVolume names the volume subpathMount derives for base and removes
// it when the test ends. Call it before creating a container that mounts
// base, so the removal runs after that container's destroy.
func subpathVolume(t *testing.T, d *Docker, base string) string {
	t.Helper()
	name := subpathMount(Mount{HostPath: base, Subpath: loginRel, ContainerPath: loginTarget}).Source
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := d.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			t.Errorf("cleanup VolumeRemove(%s) error: %v", name, err)
		}
	})
	return name
}

func subpathSpec(tag string, mounts ...Mount) Spec {
	return Spec{
		Name:    fmt.Sprintf("it-subpath-%s-%d", tag, time.Now().UnixNano()),
		Image:   testImage,
		Command: []string{"/bin/sh", "-c", "exec sleep 300"},
		Mounts:  mounts,
	}
}

// sh runs script in the container and returns its exit code and combined
// output.
func sh(t *testing.T, d *Docker, id ID, script string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	code, stdout, stderr, err := d.Exec(ctx, id, []string{"/bin/sh", "-c", script}, "")
	if err != nil {
		t.Fatalf("exec %q error: %v (stdout=%q stderr=%q)", script, err, stdout, stderr)
	}
	return code, stdout + stderr
}

func mustSh(t *testing.T, d *Docker, id ID, script string) string {
	t.Helper()
	code, out := sh(t, d, id, script)
	if code != 0 {
		t.Fatalf("exec %q exited %d: %q", script, code, out)
	}
	return out
}

func wantContent(t *testing.T, d *Docker, id ID, path, want string) {
	t.Helper()
	if got := mustSh(t, d, id, "cat "+path); got != want {
		t.Fatalf("container %s = %q, want %q", path, got, want)
	}
}

func wantAbsent(t *testing.T, d *Docker, id ID, paths ...string) {
	t.Helper()
	for _, p := range paths {
		code, out := sh(t, d, id, fmt.Sprintf(`if [ -e %[1]s ] || [ -L %[1]s ]; then ls -la %[1]s; exit 1; fi`, p))
		if code != 0 {
			t.Fatalf("container has %s, want it absent: %s", p, out)
		}
	}
}

// wantUnreadable fails if any file under /root contains any of markers.
func wantUnreadable(t *testing.T, d *Docker, id ID, markers ...string) {
	t.Helper()
	script := "grep -r -l"
	for _, m := range markers {
		script += " -e " + m
	}
	script += " /root"
	code, out := sh(t, d, id, script)
	switch code {
	case 1:
	case 0:
		t.Fatalf("files under /root contain %v: %s", markers, out)
	default:
		t.Fatalf("%q exited %d: %s", script, code, out)
	}
}

// wantReplaceRefused asserts the mounted file at target can be neither
// renamed over nor unlinked, which is what a writer falling back to an
// in-place write relies on, and that its content survives both attempts.
func wantReplaceRefused(t *testing.T, d *Docker, id ID, target, content string) {
	t.Helper()
	for _, script := range []string{
		fmt.Sprintf("printf x > %[1]s.tmp && mv -f %[1]s.tmp %[1]s", target),
		"rm -f " + target,
	} {
		code, out := sh(t, d, id, script)
		if code == 0 {
			t.Fatalf("%q succeeded on the mounted login (output %q); %s now reads %q", script, out, target, mustSh(t, d, id, "cat "+target+" 2>&1 || true"))
		}
		if !strings.Contains(strings.ToLower(out), "busy") {
			t.Errorf("%q exited %d with %q, want EBUSY (\"Resource busy\")", script, code, out)
		}
		t.Logf("%q refused: exit %d, %q", script, code, out)
		wantContent(t, d, id, target, content)
	}
}

// mountTable returns the container's /proc/self/mountinfo as (root, mount
// point) pairs.
func mountTable(t *testing.T, d *Docker, id ID) [][2]string {
	t.Helper()
	var out [][2]string
	for _, line := range strings.Split(mustSh(t, d, id, "cat /proc/self/mountinfo"), "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 {
			out = append(out, [2]string{f[3], f[4]})
		}
	}
	return out
}

// wantMountsUnderRoot asserts the container's mount points under /root are
// exactly want, that nothing from the owner's home is mounted anywhere but at
// login, and that nothing from the victim directory is mounted at all.
func wantMountsUnderRoot(t *testing.T, d *Docker, id ID, login string, want ...string) {
	t.Helper()
	var under []string
	for _, m := range mountTable(t, d, id) {
		root, point := m[0], m[1]
		if point == "/root" || strings.HasPrefix(point, "/root/") {
			under = append(under, point)
			t.Logf("mountinfo: %s mounted at %s", root, point)
		}
		if strings.Contains(root+"/", "/aether-owner-home/") && point != login {
			t.Fatalf("owner's home is mounted at %s (root %s)", point, root)
		}
		if strings.Contains(root+"/", "/aether-victim/") {
			t.Fatalf("victim directory is mounted at %s (root %s)", point, root)
		}
	}
	slices.Sort(under)
	slices.Sort(want)
	if !slices.Equal(under, want) {
		t.Fatalf("mount points under /root = %v, want %v", under, want)
	}
}

// wantHostTree asserts base holds exactly the regular files in want, with
// that content, and nothing else but their directories.
func wantHostTree(t *testing.T, base string, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(base, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, name)
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
		case entry.Type().IsRegular():
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			got[filepath.ToSlash(rel)] = string(data)
		default:
			got[filepath.ToSlash(rel)] = "<" + entry.Type().String() + ">"
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", base, err)
	}
	for rel, content := range want {
		if got[rel] != content {
			t.Errorf("%s/%s = %q, want %q", base, rel, got[rel], content)
		}
	}
	for rel, content := range got {
		if _, ok := want[rel]; !ok {
			t.Errorf("%s has unexpected %s (%q)", base, rel, content)
		}
	}
}

// removeVolume removes the volume now and fails if the engine refuses.
func removeVolume(t *testing.T, d *Docker, name string) {
	t.Helper()
	if _, err := d.cli.VolumeRemove(t.Context(), name, client.VolumeRemoveOptions{}); err != nil {
		t.Fatalf("VolumeRemove(%s) error: %v", name, err)
	}
}

// TestDockerSubpathLoginFile mounts one file of the owner's home over the
// same path in the launcher's home: the container sees the owner's login and
// the launcher's everything else, writes pass through in place both ways,
// the login cannot be replaced by rename or unlink, and neither destroying
// the container nor removing the volume touches the owner's files.
func TestDockerSubpathLoginFile(t *testing.T) {
	t.Parallel()
	d := newSubpathDocker(t)
	h := newSubpathHomes(t)
	ownerFiles := map[string]string{
		loginRel:                "owner-login",
		".claude/settings.json": "owner-settings-marker",
		".config/gh/hosts.yml":  "owner-gh-marker",
		".ssh/id":               "owner-ssh-marker",
		".gitconfig":            "owner-gitconfig-marker",
	}
	writeTree(t, h.owner, ownerFiles)
	writeTree(t, h.launcher, map[string]string{
		".gitconfig":           "launcher-gitconfig",
		".config/gh/hosts.yml": "launcher-gh",
		loginRel:               "",
	})
	volume := subpathVolume(t, d, h.owner)
	id := createContainer(t, d, subpathSpec("file", h.home(), h.login()))
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() with a file subpath login: %v", err)
	}
	hostLogin := filepath.Join(h.owner, filepath.FromSlash(loginRel))

	t.Run("only the login comes from the owner", func(t *testing.T) {
		wantContent(t, d, id, loginTarget, "owner-login")
		wantContent(t, d, id, "/root/.gitconfig", "launcher-gitconfig")
		wantContent(t, d, id, "/root/.config/gh/hosts.yml", "launcher-gh")
		wantAbsent(t, d, id, "/root/.ssh", "/root/.claude/settings.json")
		wantUnreadable(t, d, id, "owner-settings-marker", "owner-gh-marker", "owner-ssh-marker", "owner-gitconfig-marker")
		wantMountsUnderRoot(t, d, id, loginTarget, "/root", loginTarget)
	})

	t.Run("in-place writes pass through both ways", func(t *testing.T) {
		before := inode(t, hostLogin)
		mustSh(t, d, id, "printf new > "+loginTarget)
		if got := readHost(t, hostLogin); got != "new" {
			t.Fatalf("host login after an in-container write = %q, want %q", got, "new")
		}
		if err := os.WriteFile(hostLogin, []byte("host-wrote"), 0o644); err != nil {
			t.Fatal(err)
		}
		if after := inode(t, hostLogin); after != before {
			t.Fatalf("host login inode %d -> %d, want in-place writes only", before, after)
		}
		wantContent(t, d, id, loginTarget, "host-wrote")
	})

	t.Run("rename and unlink over the login are refused", func(t *testing.T) {
		wantReplaceRefused(t, d, id, loginTarget, "host-wrote")
		if got := readHost(t, hostLogin); got != "host-wrote" {
			t.Fatalf("host login after refused replacements = %q, want %q", got, "host-wrote")
		}
	})

	t.Run("volume carries the managed label", func(t *testing.T) {
		v, err := d.cli.VolumeInspect(t.Context(), volume, client.VolumeInspectOptions{})
		if err != nil {
			t.Fatalf("VolumeInspect(%s) error: %v", volume, err)
		}
		t.Logf("volume %s: driver %s, options %v, labels %v", volume, v.Volume.Driver, v.Volume.Options, v.Volume.Labels)
		if v.Volume.Labels[labelManaged] != "true" {
			t.Errorf("volume labels = %v, want %s=true", v.Volume.Labels, labelManaged)
		}
		if v.Volume.Options["device"] != h.owner {
			t.Errorf("volume device = %q, want the owner's home %q", v.Volume.Options["device"], h.owner)
		}
	})

	if err := d.Destroy(t.Context(), id); err != nil {
		t.Fatalf("Destroy() error: %v", err)
	}
	ownerFiles[loginRel] = "host-wrote"
	wantHostTree(t, h.owner, ownerFiles)
	removeVolume(t, d, volume)
	wantHostTree(t, h.owner, ownerFiles)
}

// TestDockerSubpathLoginDirectory mounts a login directory (omp's
// ~/.omp/agent): the owner's directory replaces the launcher's, its
// siblings stay the launcher's, and files inside it are created and renamed
// normally and land in the owner's home.
func TestDockerSubpathLoginDirectory(t *testing.T) {
	t.Parallel()
	d := newSubpathDocker(t)
	h := newSubpathHomes(t)
	ownerFiles := map[string]string{
		".omp/agent/agent.db":   "owner-agent-db",
		".omp/agent/config.yml": "owner-agent-config",
		".omp/cache/x":          "owner-cache-marker",
	}
	writeTree(t, h.owner, ownerFiles)
	writeTree(t, h.launcher, map[string]string{".omp/cache/y": "launcher-cache"})
	if err := os.Mkdir(filepath.Join(h.launcher, ".omp", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	volume := subpathVolume(t, d, h.owner)
	const agentDir = "/root/.omp/agent"
	id := createContainer(t, d, subpathSpec("dir", h.home(), Mount{HostPath: h.owner, Subpath: ".omp/agent", ContainerPath: agentDir}))
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() with a directory subpath login: %v", err)
	}

	wantContent(t, d, id, agentDir+"/agent.db", "owner-agent-db")
	wantContent(t, d, id, agentDir+"/config.yml", "owner-agent-config")
	wantContent(t, d, id, "/root/.omp/cache/y", "launcher-cache")
	wantAbsent(t, d, id, "/root/.omp/cache/x")
	wantUnreadable(t, d, id, "owner-cache-marker")
	wantMountsUnderRoot(t, d, id, agentDir, "/root", agentDir)

	mustSh(t, d, id, "printf created > "+agentDir+"/new.txt")
	mustSh(t, d, id, fmt.Sprintf("printf replaced > %[1]s/agent.db.tmp && mv -f %[1]s/agent.db.tmp %[1]s/agent.db", agentDir))
	if got := readHost(t, filepath.Join(h.owner, ".omp", "agent", "new.txt")); got != "created" {
		t.Fatalf("owner's agent/new.txt = %q, want %q", got, "created")
	}
	if got := readHost(t, filepath.Join(h.owner, ".omp", "agent", "agent.db")); got != "replaced" {
		t.Fatalf("owner's agent/agent.db after an in-container rename = %q, want %q", got, "replaced")
	}

	if err := d.Destroy(t.Context(), id); err != nil {
		t.Fatalf("Destroy() error: %v", err)
	}
	ownerFiles[".omp/agent/agent.db"] = "replaced"
	ownerFiles[".omp/agent/new.txt"] = "created"
	wantHostTree(t, h.owner, ownerFiles)
	removeVolume(t, d, volume)
	wantHostTree(t, h.owner, ownerFiles)
}

// TestDockerSubpathLoginInOwnHome mounts a member's own login in place
// over their own home bind: the file stays one inode, writable in place and
// not replaceable.
func TestDockerSubpathLoginInOwnHome(t *testing.T) {
	t.Parallel()
	d := newSubpathDocker(t)
	h := newSubpathHomes(t)
	writeTree(t, h.owner, map[string]string{loginRel: "own-login"})
	subpathVolume(t, d, h.owner)
	id := createContainer(t, d, subpathSpec("own", Mount{HostPath: h.owner, ContainerPath: "/root"}, h.login()))
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() with the login mounted over its own home: %v", err)
	}
	hostLogin := filepath.Join(h.owner, filepath.FromSlash(loginRel))
	before := inode(t, hostLogin)

	wantContent(t, d, id, loginTarget, "own-login")
	mustSh(t, d, id, "printf own-new > "+loginTarget)
	if got := readHost(t, hostLogin); got != "own-new" {
		t.Fatalf("host login after an in-container write = %q, want %q", got, "own-new")
	}
	if after := inode(t, hostLogin); after != before {
		t.Fatalf("host login inode %d -> %d after an in-container write", before, after)
	}
	wantReplaceRefused(t, d, id, loginTarget, "own-new")
	if after := inode(t, hostLogin); after != before {
		t.Fatalf("host login inode %d -> %d after refused replacements", before, after)
	}
}

// wantStartRefused creates and starts a container whose login subpath
// leaves the owner's home, and fails if it ever runs. The victim's content
// must never be readable, so a container that does start is read before
// the test fails.
func wantStartRefused(t *testing.T, d *Docker, spec Spec) {
	t.Helper()
	id, err := d.Create(t.Context(), spec)
	if err != nil {
		t.Logf("engine refused at create: %v", err)
		return
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if derr := d.Destroy(ctx, id); derr != nil {
			t.Errorf("cleanup Destroy() error: %v", derr)
		}
	})
	if err = d.Start(t.Context(), id); err == nil {
		_, out := sh(t, d, id, "cat "+loginTarget)
		t.Fatalf("container started with an escaping login subpath; %s reads %q", loginTarget, out)
	}
	t.Logf("engine refused at start: %v", err)
	info, err := d.cli.ContainerInspect(t.Context(), string(id), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if info.Container.State.Running {
		t.Fatalf("container is running after a refused start")
	}
}

// TestDockerSubpathSymlinkEscapeRefused plants symlinks that lead a login
// subpath out of the owner's home. The engine must refuse to start the
// container.
func TestDockerSubpathSymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	d := newSubpathDocker(t)
	cases := map[string]func(t *testing.T, h subpathHomes){
		"final component to a sibling directory": func(t *testing.T, h subpathHomes) {
			writeTree(t, h.victim, map[string]string{"secret": "victim-secret"})
			plantLink(t, "../../aether-victim/secret", filepath.Join(h.owner, filepath.FromSlash(loginRel)))
		},
		"intermediate component to a sibling directory": func(t *testing.T, h subpathHomes) {
			writeTree(t, h.victim, map[string]string{".credentials.json": "victim-secret"})
			plantLink(t, "../aether-victim", filepath.Join(h.owner, ".claude"))
		},
		"final component to an absolute host path": func(t *testing.T, h subpathHomes) {
			plantLink(t, "/etc/hostname", filepath.Join(h.owner, filepath.FromSlash(loginRel)))
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newSubpathHomes(t)
			writeTree(t, h.launcher, map[string]string{loginRel: ""})
			plant(t, h)
			subpathVolume(t, d, h.owner)
			wantStartRefused(t, d, subpathSpec("escape", h.home(), h.login()))
		})
	}
}

// TestDockerSubpathSymlinkWithinBase documents what the engine does with a
// login symlink that stays inside the owner's home (the server refuses it
// before this point). Either outcome is allowed; exposing anything outside
// the owner's home is not.
func TestDockerSubpathSymlinkWithinBase(t *testing.T) {
	t.Parallel()
	d := newSubpathDocker(t)
	h := newSubpathHomes(t)
	writeTree(t, h.owner, map[string]string{".other/real.json": "owner-real"})
	writeTree(t, h.victim, map[string]string{"secret": "victim-secret"})
	plantLink(t, "../.other/real.json", filepath.Join(h.owner, filepath.FromSlash(loginRel)))
	writeTree(t, h.launcher, map[string]string{loginRel: ""})
	subpathVolume(t, d, h.owner)
	id := createContainer(t, d, subpathSpec("inbase", h.home(), h.login()))
	if err := d.Start(t.Context(), id); err != nil {
		t.Logf("engine refused a symlink inside the base: %v", err)
		return
	}
	got := mustSh(t, d, id, "cat "+loginTarget)
	if got != "owner-real" {
		t.Fatalf("login through an in-base symlink reads %q, want the owner's %q or a refused start", got, "owner-real")
	}
	wantUnreadable(t, d, id, "victim-secret")
	wantMountsUnderRoot(t, d, id, loginTarget, "/root", loginTarget)
	t.Logf("engine followed a symlink inside the base to %s", filepath.Join(h.owner, ".other", "real.json"))
}

// startAfterSwap starts a container whose owner's login directory was
// replaced by a symlink to the victim directory after the container was
// created. The victim's login must never be readable in it.
func startAfterSwap(t *testing.T, d *Docker, h subpathHomes, id ID) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(h.owner, ".claude")); err != nil {
		t.Fatal(err)
	}
	plantLink(t, "../aether-victim", filepath.Join(h.owner, ".claude"))
	if err := d.Start(t.Context(), id); err != nil {
		t.Logf("engine refused the swapped login at start: %v", err)
		info, ierr := d.cli.ContainerInspect(t.Context(), string(id), client.ContainerInspectOptions{})
		if ierr != nil {
			t.Fatalf("inspect: %v", ierr)
		}
		if info.Container.State.Running {
			t.Fatalf("container is running after a refused start")
		}
		return
	}
	_, out := sh(t, d, id, "cat "+loginTarget)
	if strings.Contains(out, "victim-secret") {
		t.Fatalf("victim's login is readable at %s after the swap: %q", loginTarget, out)
	}
	wantUnreadable(t, d, id, "victim-secret")
	t.Logf("container started after the swap; %s reads %q", loginTarget, out)
}

// TestDockerSubpathSwapBeforeStart replaces a validated login path with a
// symlink out of the owner's home between container create and start, and
// between a stop and a restart: the engine resolves the subpath again at
// each start, so the swap never exposes the target.
func TestDockerSubpathSwapBeforeStart(t *testing.T) {
	t.Parallel()
	d := newSubpathDocker(t)
	setup := func(t *testing.T) (subpathHomes, ID) {
		h := newSubpathHomes(t)
		writeTree(t, h.owner, map[string]string{loginRel: "owner-login"})
		writeTree(t, h.victim, map[string]string{".credentials.json": "victim-secret"})
		writeTree(t, h.launcher, map[string]string{loginRel: ""})
		subpathVolume(t, d, h.owner)
		return h, createContainer(t, d, subpathSpec("swap", h.home(), h.login()))
	}
	t.Run("after create", func(t *testing.T) {
		t.Parallel()
		h, id := setup(t)
		startAfterSwap(t, d, h, id)
	})
	t.Run("after stop", func(t *testing.T) {
		t.Parallel()
		h, id := setup(t)
		if err := d.Start(t.Context(), id); err != nil {
			t.Fatalf("first Start() error: %v", err)
		}
		wantContent(t, d, id, loginTarget, "owner-login")
		if err := d.Stop(t.Context(), id, 2*time.Second); err != nil {
			t.Fatalf("Stop() error: %v", err)
		}
		startAfterSwap(t, d, h, id)
	})
}
