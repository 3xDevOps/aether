package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	launcherImage = "aether/ada:1"
	ownerImage    = "aether/grace:1"
)

// shareEnv is a scheduler where the launcher (e.member, Ada) runs on the
// shared account of the owner (Grace). Each has a saved image of their own.
type shareEnv struct {
	*testEnv
	owner     *domain.Member
	ownerHome string
	adaHome   string
}

func newShareEnv(t *testing.T, mutate func(*Config)) *shareEnv {
	t.Helper()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses["claude"] = HarnessSpec{TUIArgs: []string{"fake-claude", "{task}"}}
		cfg.Harnesses["omp"] = HarnessSpec{TUIArgs: []string{"fake-omp", "{task}"}}
		if mutate != nil {
			mutate(cfg)
		}
	})
	owner := &domain.Member{DisplayName: "Grace", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	e.rt.mu.Lock()
	e.rt.images = map[string]string{launcherImage: "ada", ownerImage: "grace"}
	e.rt.mu.Unlock()
	for member, image := range map[domain.MemberID]string{e.member.ID: launcherImage, owner.ID: ownerImage} {
		if err := e.db.UpdateMemberImage(t.Context(), member, image); err != nil {
			t.Fatal(err)
		}
	}
	ownerHome, err := e.cfg.Homes.Path(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	adaHome, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &shareEnv{testEnv: e, owner: owner, ownerHome: ownerHome, adaHome: adaHome}
}

// ownerState is what a shared run must never reach in the owner's home,
// opencode's login under ~/.local/share among it.
var ownerState = []string{".gitconfig", ".config/gh/hosts.yml", ".ssh/id_ed25519", ".codex/auth.json", ".claude/settings.json", ".bash_history", ".local/share/opencode/auth.json"}

func writeHomeFiles(t *testing.T, home string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		name := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("secret of "+rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *shareEnv) launch(t *testing.T, harnessName string) (*domain.Run, error) {
	t.Helper()
	return e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "use "+harnessName, harnessName, domain.LaunchTUI)
}

func (e *shareEnv) containerCount() int {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	return len(e.rt.containers)
}

// exposed lists every host path a spec makes visible in the container.
func exposed(spec runtime.Spec) []string {
	out := []string{spec.WorktreeHostPath}
	for _, m := range spec.Mounts {
		out = append(out, filepath.Join(m.HostPath, m.Subpath))
	}
	return out
}

func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && filepath.IsLocal(rel)
}

// assertOwnerExposedOnlyAt fails unless the owner's home reaches the
// container through exactly one subpath mount per path, a login or a
// borrowed installation directory, and none of the owner's other state is
// under any exposed path.
func (e *shareEnv) assertOwnerExposedOnlyAt(t *testing.T, spec runtime.Spec, paths ...string) {
	t.Helper()
	var got []string
	for _, m := range spec.Mounts {
		reach := filepath.Join(m.HostPath, m.Subpath)
		if !within(reach, e.ownerHome) && !within(e.ownerHome, reach) {
			continue
		}
		if m.HostPath != e.ownerHome || m.Subpath == "" {
			t.Fatalf("mount %+v exposes the owner's home other than through a subpath", m)
		}
		got = append(got, m.Subpath)
	}
	if !slices.Equal(got, paths) {
		t.Fatalf("owner home exposed at %v, want only %v", got, paths)
	}
	for _, rel := range ownerState {
		host := filepath.Join(e.ownerHome, filepath.FromSlash(rel))
		for _, path := range exposed(spec) {
			if path != "" && within(host, path) {
				t.Fatalf("owner state %s is exposed through %s", rel, path)
			}
		}
	}
}

// A shared run is the launcher's image and home plus the harness's declared
// login from the owner's home, mounted at the same home-relative path, and
// nothing else of the owner's - for a file login and a directory login.
func TestSharedLaunchMountsOnlyTheOwnersLogin(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	writeHomeFiles(t, e.ownerHome, append([]string{".claude/.credentials.json", ".omp/agent/agent.db"}, ownerState...)...)
	for _, tc := range []struct {
		harness, login string
		dir            bool
	}{
		{"claude", ".claude/.credentials.json", false},
		{"omp", ".omp/agent", true},
	} {
		run, err := e.launch(t, tc.harness)
		if err != nil {
			t.Fatalf("%s: Launch: %v", tc.harness, err)
		}
		c := e.rt.byName(string(run.ID))
		if c == nil {
			t.Fatalf("%s: no container", tc.harness)
		}
		spec := c.spec
		if spec.Image != launcherImage {
			t.Fatalf("%s: image = %q, want the launcher's %q", tc.harness, spec.Image, launcherImage)
		}
		if len(spec.Mounts) != 2 {
			t.Fatalf("%s: mounts = %+v, want the launcher's home and one login", tc.harness, spec.Mounts)
		}
		if home := spec.Mounts[0]; home != (runtime.Mount{HostPath: e.adaHome, ContainerPath: "/root"}) {
			t.Fatalf("%s: home mount = %+v, want the launcher's home %q at /root", tc.harness, home, e.adaHome)
		}
		want := runtime.Mount{HostPath: e.ownerHome, Subpath: tc.login, ContainerPath: "/root/" + tc.login}
		if login := spec.Mounts[1]; login != want {
			t.Fatalf("%s: login mount = %+v, want %+v", tc.harness, login, want)
		}
		e.assertOwnerExposedOnlyAt(t, spec, tc.login)
		identity := e.member.GitIdentity()
		for name, value := range map[string]string{
			"GIT_AUTHOR_NAME": identity.Name, "GIT_COMMITTER_NAME": identity.Name,
			"GIT_AUTHOR_EMAIL": identity.Email, "GIT_COMMITTER_EMAIL": identity.Email,
			"AETHER_ACCOUNT_MEMBER_ID": string(e.owner.ID),
		} {
			if spec.Env[name] != value {
				t.Fatalf("%s: %s = %q, want %q", tc.harness, name, spec.Env[name], value)
			}
		}
		info, err := os.Lstat(filepath.Join(e.adaHome, filepath.FromSlash(tc.login)))
		if err != nil || info.IsDir() != tc.dir {
			t.Fatalf("%s: launcher mountpoint = %v, %v; want dir=%v", tc.harness, info, err, tc.dir)
		}
		if run.HomeMemberID != e.member.ID {
			t.Fatalf("%s: run home member = %q, want the launcher %s", tc.harness, run.HomeMemberID, e.member.ID)
		}
		sc, err := e.sched.readSidecar(run.ID)
		if err != nil || sc.LoginMember != string(e.owner.ID) {
			t.Fatalf("%s: sidecar = %+v, %v; want login %s", tc.harness, sc, err, e.owner.ID)
		}
	}

	// The owner's own launch is unchanged: their image, their whole home,
	// nothing mounted by subpath.
	own, err := e.sched.Launch(t.Context(), e.ws.ID, e.owner.ID, e.owner.ID, "own", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("owner Launch: %v", err)
	}
	spec := e.rt.byName(string(own.ID)).spec
	if spec.Image != ownerImage || len(spec.Mounts) != 1 ||
		spec.Mounts[0] != (runtime.Mount{HostPath: e.ownerHome, ContainerPath: "/root"}) {
		t.Fatalf("own launch = image %q mounts %+v, want %q and only the owner's home", spec.Image, spec.Mounts, ownerImage)
	}
}

// A harness that declares no login paths launches in the launcher's
// environment with nothing of the owner's; one whose declared login is
// missing from the owner's home is refused before any container exists.
func TestSharedLaunchWithoutOwnerLogin(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	writeHomeFiles(t, e.ownerHome, ownerState...)
	run, err := e.launch(t, "fake")
	if err != nil {
		t.Fatalf("fake Launch: %v", err)
	}
	spec := e.rt.byName(string(run.ID)).spec
	if len(spec.Mounts) != 1 || spec.Mounts[0].HostPath != e.adaHome {
		t.Fatalf("fake mounts = %+v, want only the launcher's home", spec.Mounts)
	}
	e.assertOwnerExposedOnlyAt(t, spec)

	before := e.containerCount()
	_, err = e.launch(t, "claude")
	if err == nil {
		t.Fatal("shared claude launch without the owner's login was accepted")
	}
	for _, want := range []string{"Grace", "claude", "~/.claude/.credentials.json", "environment terminal"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
	if e.containerCount() != before {
		t.Fatal("a container was created for the refused launch")
	}
	e.assertFailedRun(t, "use claude")
}

func (e *shareEnv) assertFailedRun(t *testing.T, task string) {
	t.Helper()
	runs, err := e.db.ListRunsByWorkspace(t.Context(), e.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Task == task {
			if r.Status != domain.RunFailed {
				t.Fatalf("refused run %s = %s, want failed", r.ID, r.Status)
			}
			return
		}
	}
	t.Fatalf("no run row for %q", task)
}

// A member's own harness definition runs only on their own account: on a
// shared account it is refused before any container exists, and a mission
// cannot record it. A server-wide definition of the same shape shares
// exactly its declared login path.
func TestSharedLaunchCustomHarness(t *testing.T) {
	t.Parallel()
	aider := harness.Definition{
		Name: "aider", Executable: "aider",
		TUIArgs: []string{"aider", "{task}"}, HeadlessArgs: []string{"aider", "-p", "{task}"},
		CredentialPaths: []string{"/root/.aider/auth.json"},
	}
	e := newShareEnv(t, nil)
	writeHomeFiles(t, e.ownerHome, append([]string{".aider/auth.json"}, ownerState...)...)
	storeMemberDefinition(t, e.testEnv, e.member.ID, aider)
	storeMemberDefinition(t, e.testEnv, e.owner.ID, aider)
	_, err := e.launch(t, "aider")
	if err == nil || !strings.Contains(err.Error(), "runs only on your own account") {
		t.Fatalf("shared launch of the launcher's own definition = %v, want refusal", err)
	}
	if e.containerCount() != 0 {
		t.Fatal("a container was created for the refused launch")
	}
	if err = e.sched.ValidateMissionLaunch(t.Context(), e.member.ID, e.owner.ID, "aider", domain.LaunchHeadless); err == nil || !strings.Contains(err.Error(), "runs only on your own account") {
		t.Fatalf("ValidateMissionLaunch = %v, want refusal", err)
	}
	if err = e.sched.ValidateMissionLaunch(t.Context(), e.member.ID, e.member.ID, "aider", domain.LaunchHeadless); err != nil {
		t.Fatalf("ValidateMissionLaunch on the member's own account: %v", err)
	}

	admin := newShareEnv(t, func(cfg *Config) {
		cfg.Harnesses["aider"] = HarnessSpec{
			Executable: aider.Executable, TUIArgs: aider.TUIArgs, HeadlessArgs: aider.HeadlessArgs,
			CredentialPaths: aider.CredentialPaths,
		}
	})
	writeHomeFiles(t, admin.ownerHome, append([]string{".aider/auth.json"}, ownerState...)...)
	run, err := admin.launch(t, "aider")
	if err != nil {
		t.Fatalf("shared launch of a server-wide definition: %v", err)
	}
	admin.assertOwnerExposedOnlyAt(t, admin.rt.byName(string(run.ID)).spec, ".aider/auth.json")
}

// A server-wide definition cannot share the home itself, and one naming a
// path outside it never passes startup validation.
func TestSharedLaunchRefusesInvalidLoginPaths(t *testing.T) {
	t.Parallel()
	spec := func(path string) HarnessSpec {
		return HarnessSpec{
			Executable: "aider", TUIArgs: []string{"aider", "{task}"}, HeadlessArgs: []string{"aider", "-p", "{task}"},
			CredentialPaths: []string{path},
		}
	}
	for _, bad := range []string{"/root/../etc", "/etc/passwd", "/root/.aider/../../etc"} {
		if err := validateHarnessSpec("aider", spec(bad)); err == nil {
			t.Fatalf("login path %q passed validation", bad)
		}
	}
	for _, bad := range []string{"/root", "/home/aether"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			e := newShareEnv(t, func(cfg *Config) { cfg.Harnesses["aider"] = spec(bad) })
			writeHomeFiles(t, e.ownerHome, ownerState...)
			if _, err := e.launch(t, "aider"); err == nil {
				t.Fatalf("login path %q accepted", bad)
			}
			if e.containerCount() != 0 {
				t.Fatal("a container was created for the refused launch")
			}
		})
	}
}

// A symlink in the owner's login path - the file itself, or a directory on
// the way, pointing at another member's home or the host - is refused before
// any container exists, and nothing is chowned through it.
func TestSharedLaunchRefusesSymlinkedLogin(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T, e *shareEnv, otherHome string){
		"final component to another member's login": func(t *testing.T, e *shareEnv, otherHome string) {
			replaceWithLink(t, filepath.Join(e.ownerHome, ".claude", ".credentials.json"), filepath.Join(otherHome, ".claude", ".credentials.json"))
		},
		"intermediate component, relative, to another member's home": func(t *testing.T, e *shareEnv, otherHome string) {
			replaceWithLink(t, filepath.Join(e.ownerHome, ".claude"), filepath.Join("..", filepath.Base(otherHome), ".claude"))
		},
		"intermediate component to the host": func(t *testing.T, e *shareEnv, _ string) {
			replaceWithLink(t, filepath.Join(e.ownerHome, ".claude"), "/etc")
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newShareEnv(t, nil)
			other := &domain.Member{DisplayName: "Eve", PublicKey: testPublicKey(t), Color: "#4363d8", Role: domain.RoleCollaborator}
			if err := e.db.CreateMember(t.Context(), other); err != nil {
				t.Fatal(err)
			}
			otherHome, err := e.cfg.Homes.Path(other.ID)
			if err != nil {
				t.Fatal(err)
			}
			writeHomeFiles(t, otherHome, ".claude/.credentials.json")
			writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
			plant(t, e, otherHome)
			_, err = e.launch(t, "claude")
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("Launch = %v, want a symlink refusal", err)
			}
			if e.containerCount() != 0 {
				t.Fatal("a container was created for the refused launch")
			}
		})
	}
}

func replaceWithLink(t *testing.T, name, target string) {
	t.Helper()
	if err := os.RemoveAll(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

// userRuntime resolves each image's configured user, as Docker does.
type userRuntime struct {
	*fakeRuntime
	users map[string]string
}

func (r *userRuntime) ImageUser(_ context.Context, ref string) (string, error) {
	return r.users[ref], nil
}

func withImageUsers(users map[string]string) func(*Config) {
	return func(cfg *Config) {
		cfg.Runtime = &userRuntime{fakeRuntime: cfg.Runtime.(*fakeRuntime), users: users}
	}
}

func ownerOf(t *testing.T, name string) uint32 {
	t.Helper()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Uid
}

const recipientImage = "aether/lin:1"

// addRecipient adds a second member, Lin, with a saved image of their own.
func (e *shareEnv) addRecipient(t *testing.T) *domain.Member {
	t.Helper()
	lin := &domain.Member{DisplayName: "Lin", PublicKey: testPublicKey(t), Color: "#4363d8", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(t.Context(), lin); err != nil {
		t.Fatal(err)
	}
	e.rt.mu.Lock()
	e.rt.images[recipientImage] = "lin"
	e.rt.mu.Unlock()
	if err := e.db.UpdateMemberImage(t.Context(), lin.ID, recipientImage); err != nil {
		t.Fatal(err)
	}
	return lin
}

func (e *shareEnv) ownerLaunch(t *testing.T) error {
	t.Helper()
	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.owner.ID, e.owner.ID, "own", "claude", domain.LaunchTUI)
	return err
}

// A non-root shared run hands only the owner's login path to its user. It
// never holds the owner's home against the owner: the owner's run and
// environment terminal start beside it with another mapping, and take the
// login back. A recipient's run is refused while the owner's live container
// holds the login with another mapping.
func TestSharedLaunchNonRootOwnershipAndReservation(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	users := withImageUsers(map[string]string{launcherImage: "1000:1000", ownerImage: "2000:2000"})
	e := newShareEnv(t, users)
	writeHomeFiles(t, e.ownerHome, append([]string{".claude/.credentials.json"}, ownerState...)...)
	run, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	spec := e.rt.byName(string(run.ID)).spec
	want := runtime.Mount{HostPath: e.ownerHome, Subpath: ".claude/.credentials.json", ContainerPath: "/home/aether/.claude/.credentials.json"}
	if spec.User != "1000:1000" || len(spec.Mounts) != 2 || spec.Mounts[1] != want {
		t.Fatalf("spec user %q mounts %+v, want 1000:1000 and login %+v", spec.User, spec.Mounts, want)
	}
	if got := ownerOf(t, filepath.Join(e.ownerHome, ".claude", ".credentials.json")); got != 1000 {
		t.Fatalf("owner's login owned by %d, want the run user 1000", got)
	}
	for _, rel := range append([]string{".", ".claude"}, ownerState...) {
		if got := ownerOf(t, filepath.Join(e.ownerHome, filepath.FromSlash(rel))); got != 0 {
			t.Fatalf("owner's %s chowned to %d", rel, got)
		}
	}
	if got := ownerOf(t, filepath.Join(e.adaHome, ".claude", ".credentials.json")); got != 1000 {
		t.Fatalf("launcher's mountpoint owned by %d, want 1000", got)
	}

	if _, err = e.sched.EnsureTerminal(t.Context(), e.owner.ID); err != nil {
		t.Fatalf("owner terminal beside the shared run: %v", err)
	}
	if err = e.ownerLaunch(t); err != nil {
		t.Fatalf("owner launch beside the shared run: %v", err)
	}
	if got := ownerOf(t, filepath.Join(e.ownerHome, ".claude", ".credentials.json")); got != 2000 {
		t.Fatalf("owner's login owned by %d after the owner's own run, want 2000", got)
	}

	f := newShareEnv(t, users)
	writeHomeFiles(t, f.ownerHome, ".claude/.credentials.json")
	if err = f.ownerLaunch(t); err != nil {
		t.Fatalf("owner Launch: %v", err)
	}
	// The public reason elides the middle of a long error, so this checks
	// the login it names and the conflicting mapping.
	_, err = f.launch(t, "claude")
	if err == nil || !strings.Contains(err.Error(), "login "+string(f.owner.ID)+" shares is held by live run") ||
		!strings.Contains(err.Error(), "resolved user 1000:1000") {
		t.Fatalf("shared launch with another uid beside the owner's run = %v, want reservation refusal", err)
	}
	if got := ownerOf(t, filepath.Join(f.ownerHome, ".claude", ".credentials.json")); got != 2000 {
		t.Fatalf("refused launch chowned the owner's login to %d", got)
	}
}

// A recipient's chown of the owner's login runs after its reservation. When
// the owner's container reserves the owner's home with another uid in
// between and takes the login back, that late chown is refused instead of
// handing the login back to the recipient under the owner's live container.
func TestSharedLoginChownAfterTheOwnerReservesIsRefused(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	e := newShareEnv(t, withImageUsers(map[string]string{launcherImage: "1000:1000", ownerImage: "2000:2000"}))
	writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
	run, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err = e.ownerLaunch(t); err != nil {
		t.Fatalf("owner Launch: %v", err)
	}
	login := filepath.Join(e.ownerHome, ".claude", ".credentials.json")
	if got := ownerOf(t, login); got != 2000 {
		t.Fatalf("owner's login owned by %d after the owner's run, want 2000", got)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	err = e.sched.applyLoginOwnership(entry, e.owner.ID, e.rt.byName(string(run.ID)).spec.Mounts, "1000:1000")
	if err == nil || !strings.Contains(err.Error(), "the login "+string(e.owner.ID)+" shares is held by live run") {
		t.Fatalf("late login chown = %v, want the login conflict", err)
	}
	if got := ownerOf(t, login); got != 2000 {
		t.Fatalf("late login chown handed the owner's login to %d", got)
	}
}

// startLoginChown starts a recipient's login pass, as 1000:1000, over an omp
// login directory in the owner's home deep and large enough to take a while.
// It returns once the pass has chowned the directory and has not yet reached
// last, its final entry, together with the pass's result.
func (e *shareEnv) startLoginChown(t *testing.T) (done <-chan error, login, last string) {
	t.Helper()
	login = filepath.Join(e.ownerHome, ".omp", "agent")
	deepest := filepath.Join(login, strings.Repeat("d/", 40))
	if err := os.MkdirAll(deepest, 0o700); err != nil {
		t.Fatal(err)
	}
	const files = 5000
	for i := range files {
		if err := os.WriteFile(filepath.Join(deepest, fmt.Sprintf("%04d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	last = filepath.Join(deepest, fmt.Sprintf("%04d", files-1))
	entry := &supervised{runID: "recipient", memberID: e.member.ID}
	mounts := []runtime.Mount{{HostPath: e.ownerHome, Subpath: ".omp/agent", ContainerPath: "/home/aether/.omp/agent"}}
	result := make(chan error, 1)
	go func() { result <- e.sched.applyLoginOwnership(entry, e.owner.ID, mounts, "1000:1000") }()
	for ownerOf(t, login) != 1000 {
		select {
		case err := <-result:
			t.Fatalf("login pass ended before it chowned the login: %v", err)
		default:
		}
	}
	if ownerOf(t, last) != 0 {
		t.Fatal("login pass finished before it could be observed in progress")
	}
	return result, login, last
}

// A long chown of one member's shared login holds that member's home, and
// nothing else: the rest of the scheduler keeps working during the walk.
func TestLoginChownHoldsOnlyTheOwnersHome(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	e := newShareEnv(t, nil)
	done, _, last := e.startLoginChown(t)
	if _, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "beside the chown", "fake", domain.LaunchTUI); err != nil {
		t.Fatalf("launch during another member's login chown: %v", err)
	}
	lock := e.sched.homeLock(e.owner.ID)
	held := !lock.TryLock()
	if !held {
		lock.Unlock()
	}
	// Read last: still unchowned, so both checks above ran during the walk.
	if ownerOf(t, last) != 0 {
		t.Fatal("the launch waited for another member's login chown")
	}
	if !held {
		t.Fatal("the login chown runs without the owner's home lock")
	}
	if err := <-done; err != nil {
		t.Fatalf("login chown: %v", err)
	}
}

// The owner's own ownership pass waits for a recipient's login chown in
// progress on the owner's home, then takes the login back.
func TestOwnerPassWaitsForARecipientsLoginChown(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	e := newShareEnv(t, nil)
	done, login, last := e.startLoginChown(t)
	owner := make(chan error, 1)
	go func() {
		owner <- e.sched.applyRunOwnership(nil, &domain.Run{}, e.owner.ID,
			[]runtime.Mount{{HostPath: e.ownerHome, ContainerPath: "/home/aether"}}, "2000:2000")
	}()
	for finished := false; !finished; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("login chown: %v", err)
			}
			finished = true
		default:
			// Read in this order: last is still unchowned after home was
			// read, so the recipient's walk was in progress at that read.
			if ownerOf(t, e.ownerHome) == 2000 && ownerOf(t, last) == 0 {
				t.Fatal("the owner's pass ran during the recipient's login chown")
			}
		}
	}
	if err := <-owner; err != nil {
		t.Fatalf("owner's pass: %v", err)
	}
	for _, name := range []string{login, last} {
		if got := ownerOf(t, name); got != 2000 {
			t.Fatalf("%s owned by %d after the owner's pass, want 2000", name, got)
		}
	}
}

// A recipient's run on the owner's login never blocks the owner's
// environment terminal with another mapping: not when a surviving terminal's
// metadata is resolved, and not when its ownership is rechecked. The
// recipient's own home stays held against a terminal of theirs.
func TestOwnerTerminalIsNotBlockedByARecipientsLogin(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	e.sched.mu.Lock()
	e.sched.syncRunUserReservationsLocked()
	e.sched.credentialUsers[&credentialUserReservation{home: e.member.ID, login: e.owner.ID, user: "1000:1000", owner: "live run recipient"}] = struct{}{}
	e.sched.mu.Unlock()
	owner := &terminalSupervision{member: e.owner.ID}
	if err := e.sched.resolveTerminalMetadata(owner, "2000:2000", "/home/aether"); err != nil || owner.ownershipBlocked {
		t.Fatalf("owner's terminal metadata beside a recipient's run = %v (blocked %v), want accepted", err, owner.ownershipBlocked)
	}
	if err := e.sched.checkTerminalOwnership(owner); err != nil {
		t.Fatalf("owner's terminal ownership beside a recipient's run: %v", err)
	}
	recipient := &terminalSupervision{member: e.member.ID}
	if err := e.sched.resolveTerminalMetadata(recipient, "2000:2000", "/home/aether"); err == nil || !recipient.ownershipBlocked {
		t.Fatalf("recipient's terminal with another mapping = %v, want a conflict", err)
	}
}

// Two recipients' runs on one login must share a mapping, so neither flips
// the login's owner under the other.
func TestSharedLaunchRecipientsShareOneMapping(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	for _, tc := range []struct {
		name, second string
		allowed      bool
	}{
		{"different uid", "3000:3000", false},
		{"same uid", "1000:1000", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newShareEnv(t, withImageUsers(map[string]string{launcherImage: "1000:1000", recipientImage: tc.second}))
			lin := e.addRecipient(t)
			writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
			if _, err := e.launch(t, "claude"); err != nil {
				t.Fatalf("first recipient Launch: %v", err)
			}
			_, err := e.sched.Launch(t.Context(), e.ws.ID, lin.ID, e.owner.ID, "second", "claude", domain.LaunchTUI)
			if tc.allowed && err != nil {
				t.Fatalf("second recipient with the same mapping: %v", err)
			}
			if !tc.allowed && (err == nil || !strings.Contains(err.Error(), "shares is held by live run")) {
				t.Fatalf("second recipient with another mapping = %v, want reservation refusal", err)
			}
			if got := ownerOf(t, filepath.Join(e.ownerHome, ".claude", ".credentials.json")); got != 1000 {
				t.Fatalf("owner's login owned by %d, want the first recipient's 1000", got)
			}
		})
	}
}

// After a restart, a shared run's sidecar restores the same rules: it holds
// the launcher's home and the login against other recipients, never the
// owner's home against the owner.
func TestRecoveredSharedRunReservations(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	e := newShareEnv(t, withImageUsers(map[string]string{launcherImage: "1000:1000"}))
	writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
	run, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	if err := s2.recoverRuns(t.Context()); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	held := "is held by live run " + string(run.ID)
	for name, tc := range map[string]struct {
		entry *supervised
		want  string
	}{
		"launcher's home":  {&supervised{runID: "run-home", memberID: e.member.ID}, "home " + string(e.member.ID) + " is reserved by live run " + string(run.ID)},
		"other recipient":  {&supervised{runID: "run-login", memberID: "lin", loginMember: e.owner.ID}, held},
		"owner's own home": {&supervised{runID: "run-owner", memberID: e.owner.ID}, ""},
	} {
		err := s2.reserveRunUser(tc.entry, "2000:2000", true)
		if tc.want == "" && err != nil {
			t.Fatalf("%s after restart: %v", name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s after restart = %v, want %q", name, err, tc.want)
		}
	}
}

// A shared run whose sidecar is lost gets a synthetic one at restart. Its
// owner holds the launcher's home, recorded on the run row, and the account
// owner's login, never the account owner's home, so the owner's own
// containers still start.
func TestRecoveredSharedRunWithoutSidecarHoldsTheLaunchersHome(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	ctx := t.Context()
	closed, err := e.launch(t, "fake")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err = e.sched.CloseRun(ctx, closed.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	unstarted, err := e.launch(t, "fake")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	if err = e.db.UpdateRunStatus(ctx, unstarted.ID, domain.RunProvisioning, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*domain.Run{closed, unstarted} {
		if err = os.Remove(e.sched.sidecarPath(run.ID)); err != nil {
			t.Fatal(err)
		}
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = &creationKeyFailureRuntime{Runtime: e.rt, findErr: errors.New("runtime API unavailable")}
	if err = s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	for _, run := range []*domain.Run{closed, unstarted} {
		s2.mu.Lock()
		entry := s2.runs[run.ID]
		s2.mu.Unlock()
		if entry == nil || !entry.destroyPending || entry.memberID != e.member.ID || entry.loginMember != e.owner.ID {
			t.Fatalf("synthetic owner of %s = %+v, want it on the launcher's home %s and the login of %s", run.ID, entry, e.member.ID, e.owner.ID)
		}
	}
	if err = s2.reserveRunUser(&supervised{runID: "run-owner", memberID: e.owner.ID}, "2000:2000", true); err != nil {
		t.Fatalf("owner's own container beside the synthetic owners: %v", err)
	}
	err = s2.reserveRunUser(&supervised{runID: "run-home", memberID: e.member.ID}, "2000:2000", true)
	if err == nil || !strings.Contains(err.Error(), "home "+string(e.member.ID)+" is reserved") {
		t.Fatalf("launcher's home beside the synthetic owners = %v, want reservation refusal", err)
	}
}

// A shared run whose sidecar is lost, and whose container the runtime cannot
// yet confirm gone, keeps the owner's login from another recipient's uid. The
// owner's own run and terminal still start, and the login is released once
// the container is confirmed gone.
func TestRecoveredSharedRunWithoutSidecarHoldsTheOwnersLogin(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	e := newShareEnv(t, withImageUsers(map[string]string{launcherImage: "1000:1000", ownerImage: "2000:2000", recipientImage: "3000:3000"}))
	lin := e.addRecipient(t)
	ctx := t.Context()
	writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
	closed, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err = e.sched.CloseRun(ctx, closed.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	unstarted, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	if err = e.db.UpdateRunStatus(ctx, unstarted.ID, domain.RunProvisioning, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	runs := []*domain.Run{closed, unstarted}
	for _, run := range runs {
		if err = os.Remove(e.sched.sidecarPath(run.ID)); err != nil {
			t.Fatal(err)
		}
	}
	retry := &creationKeyFailureRuntime{Runtime: e.rt, findErr: errors.New("runtime API unavailable")}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = struct {
		runtime.Runtime
		imageUserResolver
	}{retry, e.cfg.Runtime.(imageUserResolver)}
	if err = s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}

	_, err = s2.Launch(ctx, e.ws.ID, lin.ID, e.owner.ID, "second", "claude", domain.LaunchTUI)
	if err == nil || !strings.Contains(err.Error(), "shares is held by live run") {
		t.Fatalf("second recipient beside the recovered runs = %v, want reservation refusal", err)
	}
	// The probe uses the owner's mapping, so once the owner's containers run
	// only the recovered owners block it.
	probe := func() error {
		return s2.reserveRunUser(&supervised{runID: "run-probe", memberID: lin.ID, loginMember: e.owner.ID}, "2000:2000", true)
	}
	if err = probe(); err == nil || !strings.Contains(err.Error(), "shares is held by live run") {
		t.Fatalf("login beside the recovered runs = %v, want reservation refusal", err)
	}

	// The owner's terminal and run look up their own creation keys. Nothing
	// retries the recovered owners until the sweep below.
	retry.setFindErr(nil)
	if _, err = s2.EnsureTerminal(ctx, e.owner.ID); err != nil {
		t.Fatalf("owner terminal beside the recovered runs: %v", err)
	}
	if _, err = s2.Launch(ctx, e.ws.ID, e.owner.ID, e.owner.ID, "own", "claude", domain.LaunchTUI); err != nil {
		t.Fatalf("owner launch beside the recovered runs: %v", err)
	}
	s2.mu.Lock()
	recovered := s2.runs[closed.ID] != nil && s2.runs[unstarted.ID] != nil
	s2.mu.Unlock()
	if !recovered {
		t.Fatal("recovered owners released before the sweep")
	}

	s2.sweepRetained(ctx)
	waitFor(t, "recovered owners released", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		return s2.runs[closed.ID] == nil && s2.runs[unstarted.ID] == nil
	})
	for _, run := range runs {
		if e.rt.byName(string(run.ID)) != nil {
			t.Fatalf("container of %s survived its release", run.ID)
		}
	}
	if err = probe(); err != nil {
		t.Fatalf("login after the recovered containers are gone: %v", err)
	}
}

// A container created before account shares were narrowed mounts the
// owner's whole home: it stays supervised against that home, but it is never
// relaunched, across restarts and whatever its sidecar says, including a
// home_member key an earlier build wrote. A narrowed shared run relaunches
// normally.
func TestLegacySharedRunIsNotRelaunched(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
	legacy, err := e.launch(t, "fake")
	if err != nil {
		t.Fatalf("Launch legacy: %v", err)
	}
	narrowed, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("Launch narrowed: %v", err)
	}
	for _, run := range []*domain.Run{legacy, narrowed} {
		if err = e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
			t.Fatalf("CloseRun: %v", err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunMerged)
	}
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	e.clearHomeMember(t, legacy.ID)
	path := e.sched.sidecarPath(legacy.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["home_member"] = string(e.member.ID)
	if data, err = json.Marshal(fields); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var last *Scheduler
	for restart := 1; restart <= 2; restart++ {
		s := e.newScheduler(t, e.rt, newFakePTY())
		if err = s.recoverRuns(t.Context()); err != nil {
			t.Fatalf("restart %d: recoverRuns: %v", restart, err)
		}
		_, err = s.Relaunch(t.Context(), legacy.ID, e.member.ID)
		if err == nil || !strings.Contains(err.Error(), "predates the narrowed account share") {
			t.Fatalf("restart %d: Relaunch legacy = %v, want refusal", restart, err)
		}
		s.mu.Lock()
		entry := s.runs[legacy.ID]
		var werr error
		if entry != nil {
			werr = s.writeSidecar(entry.sidecar())
		}
		s.mu.Unlock()
		if entry == nil || entry.memberID != e.owner.ID {
			t.Fatalf("restart %d: legacy entry = %+v, want it supervised against the owner's home", restart, entry)
		}
		if werr != nil {
			t.Fatal(werr)
		}
		last = s
		if restart == 1 {
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := last.Relaunch(t.Context(), narrowed.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch narrowed: %v", err)
	}
}

// clearHomeMember makes run a row from before account shares were narrowed
// (store migration v46), when a run's container mounted its account's whole
// home.
func (e *testEnv) clearHomeMember(t *testing.T, run domain.RunID) {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(filepath.Dir(e.cfg.StateDir), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err = raw.ExecContext(t.Context(), `UPDATE runs SET home_member_id = NULL WHERE id = ?`, run); err != nil {
		t.Fatal(err)
	}
}

type recordingProfiles struct {
	mu      sync.Mutex
	members []string
}

func (p *recordingProfiles) Latest(_ context.Context, member, _ string) (domain.ProfileSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.members = append(p.members, member)
	return domain.ProfileSnapshot{}, store.ErrNotFound
}

func (p *recordingProfiles) PinRun(context.Context, domain.RunID, domain.ProfileSnapshotID) error {
	return nil
}

// A shared run uses the launcher's harness config.
func TestSharedRunPinsLauncherProfile(t *testing.T) {
	t.Parallel()
	profiles := &recordingProfiles{}
	e := newShareEnv(t, func(cfg *Config) { cfg.Profiles = profiles })
	if _, err := e.launch(t, "fake"); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	profiles.mu.Lock()
	pinned := slices.Clone(profiles.members)
	profiles.mu.Unlock()
	if !slices.Equal(pinned, []string{string(e.member.ID)}) {
		t.Fatalf("profile snapshot read for %v, want the launcher %s", pinned, e.member.ID)
	}
}

// A borrowed Claude Code login starts signed in: the launcher's own
// ~/.claude.json gets setup marked complete, keeping whatever it already
// says, and is otherwise left to Claude Code. Nothing is written for a
// launcher's own login, for a harness without such state, or over a file
// that is not a JSON object.
func TestBorrowedClaudeLoginMarksSetupComplete(t *testing.T) {
	t.Parallel()
	read := func(t *testing.T, home string) (map[string]any, os.FileMode) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
		if err != nil {
			t.Fatalf("read ~/.claude.json: %v", err)
		}
		info, err := os.Stat(filepath.Join(home, ".claude.json"))
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatalf("~/.claude.json = %q: %v", data, err)
		}
		return state, info.Mode().Perm()
	}

	e := newShareEnv(t, nil)
	writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json", ".claude.json")
	e.launchSpec(t, "claude")
	state, mode := read(t, e.adaHome)
	if len(state) != 1 || state["hasCompletedOnboarding"] != true || mode != 0o600 {
		t.Fatalf("launcher ~/.claude.json = %v mode %o, want only hasCompletedOnboarding=true, mode 0600", state, mode)
	}

	f := newShareEnv(t, nil)
	writeHomeFiles(t, f.ownerHome, ".claude/.credentials.json")
	if err := os.WriteFile(filepath.Join(f.adaHome, ".claude.json"), []byte(`{"theme":"light","projects":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(f.adaHome, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.launchSpec(t, "claude")
	state, _ = read(t, f.adaHome)
	after, err := os.Stat(filepath.Join(f.adaHome, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if state["theme"] != "light" || state["hasCompletedOnboarding"] != true || len(state) != 3 {
		t.Fatalf("launcher ~/.claude.json = %v, want the existing keys plus hasCompletedOnboarding", state)
	}
	if !os.SameFile(before, after) || before.Sys().(*syscall.Stat_t).Ino != after.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("~/.claude.json was replaced rather than rewritten in place")
	}

	// Setup started but never finished, or recorded as not done, is marked
	// done too: Claude Code runs its wizard for anything but true.
	i := newShareEnv(t, nil)
	writeHomeFiles(t, i.ownerHome, ".claude/.credentials.json")
	if err := os.WriteFile(filepath.Join(i.adaHome, ".claude.json"), []byte(`{"hasCompletedOnboarding":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	i.launchSpec(t, "claude")
	if state, _ := read(t, i.adaHome); state["hasCompletedOnboarding"] != true {
		t.Fatalf("launcher ~/.claude.json = %v, want hasCompletedOnboarding true", state)
	}

	// A file the CLI owns in a shape Aether does not rewrite - not a JSON
	// object, or too large - is left alone and the launch still starts.
	g := newShareEnv(t, nil)
	writeHomeFiles(t, g.ownerHome, ".claude/.credentials.json")
	if err := os.WriteFile(filepath.Join(g.adaHome, ".claude.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.launchSpec(t, "claude")
	if data, _ := os.ReadFile(filepath.Join(g.adaHome, ".claude.json")); string(data) != "[]" {
		t.Fatalf("a non-object ~/.claude.json was rewritten to %q", data)
	}
	j := newShareEnv(t, nil)
	writeHomeFiles(t, j.ownerHome, ".claude/.credentials.json")
	large := []byte(`{"pad":"` + strings.Repeat("x", 5<<20) + `"}`)
	if err := os.WriteFile(filepath.Join(j.adaHome, ".claude.json"), large, 0o600); err != nil {
		t.Fatal(err)
	}
	j.launchSpec(t, "claude")
	if info, err := os.Stat(filepath.Join(j.adaHome, ".claude.json")); err != nil || info.Size() != int64(len(large)) {
		t.Fatalf("an oversized ~/.claude.json was changed: %v %v", info, err)
	}

	h := newShareEnv(t, nil)
	writeHomeFiles(t, h.ownerHome, ".claude/.credentials.json", ".omp/agent/agent.db")
	h.launchSpec(t, "omp")
	if _, err := os.Lstat(filepath.Join(h.adaHome, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("omp launch touched ~/.claude.json: %v", err)
	}
	if _, err := h.sched.Launch(t.Context(), h.ws.ID, h.owner.ID, h.owner.ID, "own", "claude", domain.LaunchTUI); err != nil {
		t.Fatalf("owner Launch: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(h.ownerHome, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("an own-account launch wrote ~/.claude.json: %v", err)
	}
}
