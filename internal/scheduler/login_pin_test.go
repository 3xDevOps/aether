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

// A member who shares their account gets their own Claude login mounted in
// place in their own run and environment terminal, created empty when it is
// missing, and nothing else beyond what a non-sharer gets.
func TestSharerContainersPinTheirOwnLogin(t *testing.T) {
	t.Parallel()
	e := newSharerEnv(t)
	spec := e.ownRun(t)
	if want := []runtime.Mount{{HostPath: e.adaHome, ContainerPath: "/root"}, e.pin()}; !slices.Equal(spec.Mounts, want) {
		t.Fatalf("sharer run mounts = %+v, want %+v", spec.Mounts, want)
	}
	info, err := os.Lstat(filepath.Join(e.adaHome, claudeLogin))
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("login stub = %v, %v; want an empty 0600 file", info, err)
	}

	terminal := e.terminal(t)
	if got := subpathMounts(terminal); !slices.Equal(got, []runtime.Mount{e.pin()}) {
		t.Fatalf("sharer terminal subpath mounts = %+v, want only %+v", got, e.pin())
	}
	plain := newShareEnv(t, nil).terminal(t)
	if len(terminal.Mounts) != len(plain.Mounts)+1 {
		t.Fatalf("sharer terminal mounts = %+v, want a non-sharer's %+v and the pin", terminal.Mounts, plain.Mounts)
	}
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

// A member with no outgoing share gets exactly the plan they always had:
// their home, and nothing created in it.
func TestNonSharerContainersAreUnchanged(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	if err := e.db.ShareAccount(t.Context(), e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	spec := e.ownRun(t)
	if want := []runtime.Mount{{HostPath: e.adaHome, ContainerPath: "/root"}}; !slices.Equal(spec.Mounts, want) {
		t.Fatalf("non-sharer run mounts = %+v, want %+v", spec.Mounts, want)
	}
	if got := subpathMounts(e.terminal(t)); len(got) != 0 {
		t.Fatalf("non-sharer terminal subpath mounts = %+v, want none", got)
	}
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
	home := runtime.Mount{HostPath: e.adaHome, ContainerPath: "/root"}

	run, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("claude Launch: %v", err)
	}
	foreign := runtime.Mount{HostPath: e.ownerHome, Subpath: claudeLogin, ContainerPath: "/root/" + claudeLogin}
	if got := e.rt.byName(string(run.ID)).spec.Mounts; !slices.Equal(got, []runtime.Mount{home, foreign}) {
		t.Fatalf("claude mounts = %+v, want %+v", got, []runtime.Mount{home, foreign})
	}

	run, err = e.launch(t, "codex")
	if err != nil {
		t.Fatalf("codex Launch: %v", err)
	}
	codex := runtime.Mount{HostPath: e.ownerHome, Subpath: ".codex/auth.json", ContainerPath: "/root/.codex/auth.json"}
	if got := e.rt.byName(string(run.ID)).spec.Mounts; !slices.Equal(got, []runtime.Mount{home, codex, e.pin()}) {
		t.Fatalf("codex mounts = %+v, want %+v", got, []runtime.Mount{home, codex, e.pin()})
	}
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

// LoginMissing answers as a launch would, without touching either home.
func TestLoginMissingMatchesLaunch(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	missing := func(member, account domain.MemberID, name string) bool {
		t.Helper()
		got, err := e.sched.LoginMissing(t.Context(), member, account, name)
		if err != nil {
			t.Fatalf("LoginMissing(%s): %v", name, err)
		}
		return got
	}
	aider := harness.Definition{
		Name: "aider", Executable: "aider",
		TUIArgs: []string{"aider", "{task}"}, HeadlessArgs: []string{"aider", "-p", "{task}"},
	}
	storeMemberDefinition(t, e.testEnv, e.member.ID, aider)

	if !missing(e.member.ID, e.owner.ID, "claude") {
		t.Fatal("claude without the owner's login is not reported missing")
	}
	if _, err := e.launch(t, "claude"); err == nil {
		t.Fatal("launch without the owner's login was accepted")
	}
	if !missing(e.member.ID, e.owner.ID, "aider") {
		t.Fatal("the launcher's own definition on a shared account is not reported missing")
	}
	if missing(e.member.ID, e.member.ID, "aider") {
		t.Fatal("the launcher's own definition on their own account reports a missing login")
	}
	if missing(e.member.ID, e.owner.ID, "fake") || missing(e.member.ID, e.member.ID, "claude") {
		t.Fatal("a harness without login paths, or the caller's own account, reports a missing login")
	}
	if _, err := e.sched.LoginMissing(t.Context(), e.member.ID, e.owner.ID, "nosuch"); err == nil {
		t.Fatal("unknown harness resolved")
	}

	writeHomeFiles(t, e.ownerHome, claudeLogin, ".aider/auth.json")
	aider.CredentialPaths = []string{"/root/.aider/auth.json"}
	storeMemberDefinition(t, e.testEnv, e.owner.ID, aider)
	if missing(e.member.ID, e.owner.ID, "claude") {
		t.Fatal("claude with the owner's login reported missing")
	}
	if !missing(e.member.ID, e.owner.ID, "aider") {
		t.Fatal("the launcher's own definition is launchable because the owner defined one too")
	}
	if _, err := os.Lstat(filepath.Join(e.adaHome, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("LoginMissing created .claude in the launcher's home: %v", err)
	}

	replaceWithLink(t, filepath.Join(e.ownerHome, claudeLogin), filepath.Join(e.ownerHome, ".aider/auth.json"))
	if !missing(e.member.ID, e.owner.ID, "claude") {
		t.Fatal("symlinked owner login is not reported missing")
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
	if missing, err := e.sched.LoginMissing(t.Context(), e.owner.ID, e.member.ID, "claude"); err != nil || !missing {
		t.Fatalf("LoginMissing on the placeholder = %v, %v; want true", missing, err)
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
	if missing, err := e.sched.LoginMissing(t.Context(), e.owner.ID, e.member.ID, "claude"); err != nil || !missing {
		t.Fatalf("LoginMissing on a hard-linked login = %v, %v; want true", missing, err)
	}
	if got := subpathMounts(e.ownRun(t)); len(got) != 0 {
		t.Fatalf("sharer run subpath mounts = %+v, want none", got)
	}
}
