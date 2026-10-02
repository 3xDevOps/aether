package scheduler

import (
	"context"
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

// ownerState is what a shared run must never reach in the owner's home.
var ownerState = []string{".gitconfig", ".config/gh/hosts.yml", ".ssh/id_ed25519", ".codex/auth.json", ".claude/settings.json", ".bash_history"}

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
// container through exactly one subpath mount per login, and none of the
// owner's other state is under any exposed path.
func (e *shareEnv) assertOwnerExposedOnlyAt(t *testing.T, spec runtime.Spec, logins ...string) {
	t.Helper()
	var got []string
	for _, m := range spec.Mounts {
		reach := filepath.Join(m.HostPath, m.Subpath)
		if !within(reach, e.ownerHome) && !within(e.ownerHome, reach) {
			continue
		}
		if m.HostPath != e.ownerHome || m.Subpath == "" {
			t.Fatalf("mount %+v exposes the owner's home other than through a login subpath", m)
		}
		got = append(got, m.Subpath)
	}
	if !slices.Equal(got, logins) {
		t.Fatalf("owner home exposed at %v, want only %v", got, logins)
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
		sc, err := e.sched.readSidecar(run.ID)
		if err != nil || sc.HomeMember != string(e.member.ID) || sc.LoginMember != string(e.owner.ID) {
			t.Fatalf("%s: sidecar = %+v, %v; want home %s and login %s", tc.harness, sc, err, e.member.ID, e.owner.ID)
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
	for _, want := range []string{"Grace", "claude", "~/.claude/.credentials.json", "aether terminal"} {
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

// A member-defined harness runs the launcher's argv, while the owner's
// definition of the same name alone decides what of the owner's home is
// shared; an owner with no such definition shares nothing.
func TestSharedLaunchCustomHarness(t *testing.T) {
	t.Parallel()
	e := newShareEnv(t, nil)
	writeHomeFiles(t, e.ownerHome, append([]string{".aider/auth.json"}, ownerState...)...)
	writeHomeFiles(t, e.ownerHome, ".ssh/config")
	storeMemberDefinition(t, e.testEnv, e.member.ID, harness.Definition{
		Name: "aider", Executable: "aider",
		TUIArgs: []string{"aider", "--as-ada", "{task}"}, HeadlessArgs: []string{"aider", "-p", "{task}"},
		CredentialPaths: []string{"/root/.ssh"},
	})
	storeMemberDefinition(t, e.testEnv, e.owner.ID, harness.Definition{
		Name: "aider", Executable: "aider", ProfileRoot: "/root/.aider",
		TUIArgs: []string{"aider", "--as-grace", "{task}"}, HeadlessArgs: []string{"aider", "-p", "{task}"},
		CredentialPaths: []string{"/root/.aider/auth.json"},
	})
	run, err := e.launch(t, "aider")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	spec := e.rt.byName(string(run.ID)).spec
	if !slices.Contains(spec.Command, "--as-ada") || slices.Contains(spec.Command, "--as-grace") {
		t.Fatalf("command = %v, want the launcher's argv", spec.Command)
	}
	e.assertOwnerExposedOnlyAt(t, spec, ".aider/auth.json")

	stranger := &domain.Member{DisplayName: "Eve", PublicKey: testPublicKey(t), Color: "#4363d8", Role: domain.RoleCollaborator}
	if err = e.db.CreateMember(t.Context(), stranger); err != nil {
		t.Fatal(err)
	}
	before := e.containerCount()
	_, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, stranger.ID, "no definition", "aider", domain.LaunchTUI)
	if err == nil || !strings.Contains(err.Error(), "has none of that name") {
		t.Fatalf("launch on an account without the definition = %v, want refusal", err)
	}
	if e.containerCount() != before {
		t.Fatal("a container was created for the refused launch")
	}
}

// The owner's definition cannot share the home itself or anything outside
// it, however the path is spelled.
func TestSharedLaunchRefusesInvalidLoginPaths(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"/root", "/home/aether", "/root/../etc", "/etc/passwd", "/root/.aider/../../etc"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			e := newShareEnv(t, nil)
			writeHomeFiles(t, e.ownerHome, ownerState...)
			def := harness.Definition{
				Name: "aider", Executable: "aider",
				TUIArgs: []string{"aider", "{task}"}, HeadlessArgs: []string{"aider", "-p", "{task}"},
			}
			storeMemberDefinition(t, e.testEnv, e.member.ID, def)
			def.CredentialPaths = []string{bad}
			storeMemberDefinition(t, e.testEnv, e.owner.ID, def)
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

// A non-root shared run hands only the owner's login path to its user, and
// holds the owner's home for that mapping: a container of the owner's with
// another mapping cannot start beside it, in either order.
func TestSharedLaunchNonRootOwnershipAndReservation(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	e := newShareEnv(t, withImageUsers(map[string]string{launcherImage: "1000:1000", ownerImage: "2000:2000"}))
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

	// The public reason elides the middle of a long error, so this checks
	// the home it names and the conflicting mapping.
	_, err = e.sched.Launch(t.Context(), e.ws.ID, e.owner.ID, e.owner.ID, "own", "claude", domain.LaunchTUI)
	if err == nil || !strings.Contains(err.Error(), "home "+string(e.owner.ID)+" is reserved by live run") ||
		!strings.Contains(err.Error(), "resolved user 2000:2000") {
		t.Fatalf("owner launch with another uid beside the shared run = %v, want reservation refusal", err)
	}

	f := newShareEnv(t, withImageUsers(map[string]string{launcherImage: "1000:1000", ownerImage: "2000:2000"}))
	writeHomeFiles(t, f.ownerHome, ".claude/.credentials.json")
	if _, err = f.sched.Launch(t.Context(), f.ws.ID, f.owner.ID, f.owner.ID, "own", "claude", domain.LaunchTUI); err != nil {
		t.Fatalf("owner Launch: %v", err)
	}
	_, err = f.launch(t, "claude")
	if err == nil || !strings.Contains(err.Error(), "home "+string(f.owner.ID)+" is reserved by live run") ||
		!strings.Contains(err.Error(), "resolved user 1000:1000") {
		t.Fatalf("shared launch with another uid beside the owner's run = %v, want reservation refusal", err)
	}
	if got := ownerOf(t, filepath.Join(f.ownerHome, ".claude", ".credentials.json")); got != 2000 {
		t.Fatalf("refused launch chowned the owner's login to %d", got)
	}
}

// After a restart, a shared run's sidecar restores reservations on both the
// launcher's home and the owner's.
func TestRecoveredSharedRunReservesBothHomes(t *testing.T) {
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
	for _, home := range []domain.MemberID{e.member.ID, e.owner.ID} {
		err := s2.reserveRunUser(&supervised{runID: "run-other", memberID: home}, "2000:2000", true)
		if err == nil || !strings.Contains(err.Error(), "home "+string(home)+" is reserved by live run "+string(run.ID)) {
			t.Fatalf("conflicting uid on %s after restart = %v, want reservation refusal", home, err)
		}
	}
}

// A container created before account shares were narrowed mounts the
// owner's whole home: it stays supervised against that home, but it is never
// relaunched. A narrowed shared run relaunches normally.
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
	sc, err := e.sched.readSidecar(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	sc.HomeMember, sc.LoginMember = "", ""
	if err = e.sched.writeSidecar(sc); err != nil {
		t.Fatal(err)
	}

	s2 := e.newScheduler(t, e.rt, newFakePTY())
	_, err = s2.Relaunch(t.Context(), legacy.ID, e.member.ID)
	if err == nil || !strings.Contains(err.Error(), "predates the narrowed account share") {
		t.Fatalf("Relaunch legacy = %v, want refusal", err)
	}
	s2.mu.Lock()
	entry := s2.runs[legacy.ID]
	s2.mu.Unlock()
	if entry == nil || entry.memberID != e.owner.ID || !slices.Equal(entry.homes(), []domain.MemberID{e.owner.ID}) {
		t.Fatalf("legacy entry = %+v, want it supervised against the owner's home", entry)
	}
	if sc, err := s2.readSidecar(legacy.ID); err != nil || sc.HomeMember != "" {
		t.Fatalf("legacy sidecar = %+v, %v; want it still without a home member", sc, err)
	}
	if _, err := s2.Relaunch(t.Context(), narrowed.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch narrowed: %v", err)
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
