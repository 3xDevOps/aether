package scheduler

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const claudeLogin = ".claude/.credentials.json"

// newSharerEnv is a shareEnv where the launcher (Ada) has shared their own
// account with the owner (Grace).
func newSharerEnv(t *testing.T) *shareEnv {
	t.Helper()
	e := newShareEnv(t, func(cfg *Config) {
		cfg.Harnesses["codex"] = HarnessSpec{TUIArgs: []string{"fake-codex", "{task}"}}
	})
	if err := e.db.ShareAccount(t.Context(), e.member.ID, e.owner.ID); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *shareEnv) ownRun(t *testing.T) runtime.Spec {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "own", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("own Launch: %v", err)
	}
	return e.rt.byName(string(run.ID)).spec
}

func (e *shareEnv) terminal(t *testing.T) runtime.Spec {
	t.Helper()
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	return e.rt.byName(terminalContainerName(e.member.ID)).spec
}

func (e *shareEnv) pin() runtime.Mount {
	return runtime.Mount{HostPath: e.adaHome, Subpath: claudeLogin, ContainerPath: "/root/" + claudeLogin}
}

func subpathMounts(spec runtime.Spec) []runtime.Mount {
	var out []runtime.Mount
	for _, m := range spec.Mounts {
		if m.Subpath != "" {
			out = append(out, m)
		}
	}
	return out
}

// Login protection is independent of reconstructible cache mounts: HOME and
// cache storage belong to the launcher, while only explicit login subpaths
// may expose another account's home.
func (e *shareEnv) assertLoginMounts(t *testing.T, spec runtime.Spec, pool string, pins ...runtime.Mount) {
	t.Helper()
	home := runtime.Mount{HostPath: e.adaHome, ContainerPath: "/root"}
	if got, ok := mountFor(spec, "/root"); !ok || got != home || spec.Env["HOME"] != "/root" {
		t.Fatalf("launcher HOME = %+v, %q; want %+v", got, spec.Env["HOME"], home)
	}
	if got := subpathMounts(spec); !slices.Equal(got, pins) {
		t.Fatalf("protected login mounts = %+v, want %+v", got, pins)
	}
	cache := runtime.Mount{
		HostPath:      filepath.Join(e.cfg.Homes.CacheRoot(), string(e.member.ID), pool, "data"),
		ContainerPath: "/aether-cache",
	}
	if got, ok := mountFor(spec, cache.ContainerPath); !ok || got != cache {
		t.Fatalf("launcher cache = %+v, want %+v", got, cache)
	}
	for _, mount := range spec.Mounts {
		reach := filepath.Join(mount.HostPath, mount.Subpath)
		if (within(reach, e.ownerHome) || within(e.ownerHome, reach)) && !slices.Contains(pins, mount) {
			t.Fatalf("unexpected account-owner exposure: %+v", mount)
		}
		if (mount.ContainerPath == "/root" || strings.HasPrefix(mount.ContainerPath, "/root/")) &&
			mount != home && !slices.Contains(pins, mount) {
			t.Fatalf("unexpected mount over protected HOME: %+v", mount)
		}
		if within(mount.HostPath, e.cfg.Homes.CacheRoot()) && mount != cache {
			t.Fatalf("unexpected cache exposure: %+v", mount)
		}
	}
}

// A member who shares their account gets their own Claude login mounted in
// place in their own run and environment terminal, created empty when it is
// missing, and nothing else beyond what a non-sharer gets.
func TestSharerContainersPinTheirOwnLogin(t *testing.T) {
	t.Parallel()
	e := newSharerEnv(t)
	spec := e.ownRun(t)
	e.assertLoginMounts(t, spec, "runs", e.pin())
	info, err := os.Lstat(filepath.Join(e.adaHome, claudeLogin))
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("login stub = %v, %v; want an empty 0600 file", info, err)
	}

	terminal := e.terminal(t)
	e.assertLoginMounts(t, terminal, "terminal", e.pin())
}

// An existing login is pinned as it is: content and mode unchanged.
func TestSharerPinLeavesExistingLoginUnchanged(t *testing.T) {
	t.Parallel()
	e := newSharerEnv(t)
	name := filepath.Join(e.adaHome, claudeLogin)
	writeHomeFiles(t, e.adaHome, claudeLogin)
	if err := os.Chmod(name, 0o640); err != nil {
		t.Fatal(err)
	}
	spec := e.ownRun(t)
	if got := subpathMounts(spec); !slices.Equal(got, []runtime.Mount{e.pin()}) {
		t.Fatalf("subpath mounts = %+v, want %+v", got, e.pin())
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != "secret of "+claudeLogin {
		t.Fatalf("login content = %q, %v; want it unchanged", data, err)
	}
	if info, err := os.Lstat(name); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("login mode = %v, %v; want 0640", info, err)
	}
}

// A member with no outgoing share keeps their own HOME and isolated cache,
// with no login pin or credential stub created.
func TestNonSharerContainersAreUnchanged(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	if err := e.db.ShareAccount(t.Context(), e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	spec := e.ownRun(t)
	e.assertLoginMounts(t, spec, "runs")
	e.assertLoginMounts(t, e.terminal(t), "terminal")
	if _, err := os.Lstat(filepath.Join(e.adaHome, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("non-sharer's home gained .claude: %v", err)
	}
}

// A sharer's run on another member's account mounts that member's login where
// it overlaps their own pin, and still pins their own Claude login under
// another harness.
func TestSharerOnAnotherAccountPrefersForeignLogin(t *testing.T) {
	t.Parallel()
	e := newSharerEnv(t)
	writeHomeFiles(t, e.ownerHome, claudeLogin, ".codex/auth.json")

	run, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("claude Launch: %v", err)
	}
	foreign := runtime.Mount{HostPath: e.ownerHome, Subpath: claudeLogin, ContainerPath: "/root/" + claudeLogin}
	e.assertLoginMounts(t, e.rt.byName(string(run.ID)).spec, "runs", foreign)

	run, err = e.launch(t, "codex")
	if err != nil {
		t.Fatalf("codex Launch: %v", err)
	}
	codex := runtime.Mount{HostPath: e.ownerHome, Subpath: ".codex/auth.json", ContainerPath: "/root/.codex/auth.json"}
	e.assertLoginMounts(t, e.rt.byName(string(run.ID)).spec, "runs", codex, e.pin())
}

// A sharer's own login path that cannot be shared is left unpinned, and the
// run and terminal still start.
func TestSharerUnshareableLoginIsNotPinned(t *testing.T) {
	t.Parallel()
	for name, plant := range map[string]func(t *testing.T, home string){
		"symlinked file": func(t *testing.T, home string) {
			writeHomeFiles(t, home, ".elsewhere")
			if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			replaceWithLink(t, filepath.Join(home, claudeLogin), filepath.Join(home, ".elsewhere"))
		},
		"symlinked directory": func(t *testing.T, home string) {
			writeHomeFiles(t, home, ".elsewhere/.credentials.json")
			replaceWithLink(t, filepath.Join(home, ".claude"), ".elsewhere")
		},
		"directory": func(t *testing.T, home string) {
			if err := os.MkdirAll(filepath.Join(home, claudeLogin), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newSharerEnv(t)
			plant(t, e.adaHome)
			if got := subpathMounts(e.ownRun(t)); len(got) != 0 {
				t.Fatalf("run subpath mounts = %+v, want none", got)
			}
			if got := subpathMounts(e.terminal(t)); len(got) != 0 {
				t.Fatalf("terminal subpath mounts = %+v, want none", got)
			}
		})
	}
}

// Revoking the last share drops the pin from the next container.
func TestRevokedShareDropsThePin(t *testing.T) {
	t.Parallel()
	e := newSharerEnv(t)
	if got := subpathMounts(e.ownRun(t)); len(got) != 1 {
		t.Fatalf("shared run subpath mounts = %+v, want the pin", got)
	}
	if err := e.db.RevokeAccountShare(t.Context(), e.member.ID, e.owner.ID); err != nil {
		t.Fatal(err)
	}
	if got := subpathMounts(e.ownRun(t)); len(got) != 0 {
		t.Fatalf("run after revoke subpath mounts = %+v, want none", got)
	}
}

// CheckSharedLaunch answers as a launch would, and says why a launch would
// be refused, without touching a file in either home.
func TestCheckSharedLaunchMatchesLaunch(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	check := func(member, account domain.MemberID, name string) SharedLaunch {
		t.Helper()
		got, refusal, err := e.sched.CheckSharedLaunch(t.Context(), member, account, name)
		if err != nil || refusal != "" {
			t.Fatalf("CheckSharedLaunch(%s) = %v, %q, %v", name, got, refusal, err)
		}
		return got
	}
	aider := harness.Definition{
		Name: "aider", Executable: "aider",
		TUIArgs: []string{"aider", "{task}"}, HeadlessArgs: []string{"aider", "-p", "{task}"},
	}
	storeMemberDefinition(t, e.testEnv, e.member.ID, aider)
	// The launcher's own definition named like a server-wide one: a launch
	// resolves the server-wide claude, so its refusal is the owner's login.
	ownClaude := aider
	ownClaude.Name = "claude"
	storeMemberDefinition(t, e.testEnv, e.member.ID, ownClaude)

	if got := check(e.member.ID, e.owner.ID, "claude"); got != SharedLoginMissing {
		t.Fatalf("claude without the owner's login = %v, want SharedLoginMissing", got)
	}
	if _, err := e.launch(t, "claude"); err == nil || !strings.Contains(err.Error(), "is not logged in to claude") {
		t.Fatalf("launch without the owner's login = %v, want the not-logged-in refusal", err)
	}
	if got := check(e.member.ID, e.owner.ID, "aider"); got != SharedOwnDefinitionOnly {
		t.Fatalf("the launcher's own definition on a shared account = %v, want SharedOwnDefinitionOnly", got)
	}
	if got := check(e.member.ID, e.member.ID, "aider"); got != SharedLaunchable {
		t.Fatalf("the launcher's own definition on their own account = %v, want SharedLaunchable", got)
	}
	if check(e.member.ID, e.owner.ID, "fake") != SharedLaunchable || check(e.member.ID, e.member.ID, "claude") != SharedLaunchable {
		t.Fatal("a harness without login paths, or the caller's own account, is refused")
	}
	if _, _, err := e.sched.CheckSharedLaunch(t.Context(), e.member.ID, e.owner.ID, "nosuch"); err == nil {
		t.Fatal("unknown harness resolved")
	}

	writeHomeFiles(t, e.ownerHome, claudeLogin, ".aider/auth.json")
	aider.CredentialPaths = []string{"/root/.aider/auth.json"}
	storeMemberDefinition(t, e.testEnv, e.owner.ID, aider)
	if got := check(e.member.ID, e.owner.ID, "claude"); got != SharedLaunchable {
		t.Fatalf("claude with the owner's login = %v, want SharedLaunchable", got)
	}
	if got := check(e.member.ID, e.owner.ID, "aider"); got != SharedOwnDefinitionOnly {
		t.Fatalf("the launcher's own definition the owner also defined = %v, want SharedOwnDefinitionOnly", got)
	}
	if _, err := os.Lstat(filepath.Join(e.adaHome, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("CheckSharedLaunch created .claude in the launcher's home: %v", err)
	}

	replaceWithLink(t, filepath.Join(e.ownerHome, claudeLogin), filepath.Join(e.ownerHome, ".aider/auth.json"))
	got, refusal, err := e.sched.CheckSharedLaunch(t.Context(), e.member.ID, e.owner.ID, "claude")
	if err != nil || got != SharedLoginUnavailable || !strings.Contains(refusal, "is a symlink and cannot be shared") {
		t.Fatalf("symlinked owner login = %v, %q, %v; want SharedLoginUnavailable with the symlink refusal", got, refusal, err)
	}
	if _, err = e.launch(t, "claude"); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("launch on the symlinked login = %v, want the refusal %q", err, refusal)
	}
}

// The empty file a pin creates is a mountpoint, not a login: a recipient's
// launch on the sharer's account is refused and reported missing, while the
// sharer's own containers still pin it.
func TestSharerPlaceholderIsNotALogin(t *testing.T) {
	t.Parallel()
	e := newSharerEnv(t)
	if got := subpathMounts(e.ownRun(t)); !slices.Equal(got, []runtime.Mount{e.pin()}) {
		t.Fatalf("sharer run subpath mounts = %+v, want the pin", got)
	}
	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.owner.ID, e.member.ID, "on placeholder", "claude", domain.LaunchTUI)
	if err == nil || !strings.Contains(err.Error(), "is not logged in to claude") {
		t.Fatalf("recipient launch on the placeholder = %v, want the not-logged-in refusal", err)
	}
	if got, refusal, err := e.sched.CheckSharedLaunch(t.Context(), e.owner.ID, e.member.ID, "claude"); err != nil || got != SharedLoginMissing || refusal != "" {
		t.Fatalf("CheckSharedLaunch on the placeholder = %v, %q, %v; want SharedLoginMissing", got, refusal, err)
	}
	if got := subpathMounts(e.terminal(t)); !slices.Equal(got, []runtime.Mount{e.pin()}) {
		t.Fatalf("sharer terminal subpath mounts = %+v, want the pin", got)
	}
}

// A login file with another hard link in the sharer's home is neither shared
// nor pinned: handing it over would hand over the other name too.
func TestSharerHardLinkedLoginIsRefused(t *testing.T) {
	t.Parallel()
	e := newSharerEnv(t)
	writeHomeFiles(t, e.adaHome, ".ssh/id_ed25519")
	if err := os.MkdirAll(filepath.Join(e.adaHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(e.adaHome, ".ssh/id_ed25519"), filepath.Join(e.adaHome, claudeLogin)); err != nil {
		t.Fatal(err)
	}
	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.owner.ID, e.member.ID, "on hard link", "claude", domain.LaunchTUI)
	if err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("recipient launch on a hard-linked login = %v, want a hard-link refusal", err)
	}
	if got, refusal, err := e.sched.CheckSharedLaunch(t.Context(), e.owner.ID, e.member.ID, "claude"); err != nil || got != SharedLoginUnavailable || !strings.Contains(refusal, "has another hard link and cannot be shared") {
		t.Fatalf("CheckSharedLaunch on a hard-linked login = %v, %q, %v; want SharedLoginUnavailable with the hard-link refusal", got, refusal, err)
	}
	if got := subpathMounts(e.ownRun(t)); len(got) != 0 {
		t.Fatalf("sharer run subpath mounts = %+v, want none", got)
	}
}
