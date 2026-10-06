package memberhome

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func newLoginHomes(t *testing.T) (*Manager, string, string) {
	t.Helper()
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := manager.Path("owner")
	if err != nil {
		t.Fatal(err)
	}
	other, err := manager.Path("other")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(owner, ".claude"), filepath.Join(owner, ".omp", "agent"),
		filepath.Join(other, ".claude"), filepath.Join(other, ".omp", "agent"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{
		filepath.Join(owner, ".claude", ".credentials.json"),
		filepath.Join(other, ".claude", ".credentials.json"),
	} {
		if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return manager, owner, other
}

func TestLoginPathIsDir(t *testing.T) {
	manager, _, _ := newLoginHomes(t)
	if dir, err := manager.LoginPathIsDir("owner", ".claude/.credentials.json"); err != nil || dir {
		t.Fatalf("file login = %v, %v; want a regular file", dir, err)
	}
	if dir, err := manager.LoginPathIsDir("owner", ".omp/agent"); err != nil || !dir {
		t.Fatalf("directory login = %v, %v; want a directory", dir, err)
	}
	for _, missing := range []string{".codex/auth.json", ".claude/missing"} {
		if _, err := manager.LoginPathIsDir("owner", missing); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("missing login %s = %v, want fs.ErrNotExist", missing, err)
		}
	}
	if _, err := manager.LoginPathIsDir("owner", "."); err == nil {
		t.Fatal("the home itself was accepted as a login path")
	}
}

// A symlink anywhere on the path makes it unshareable, whether it points
// inside the home, at another member's home, or at the host.
func TestLoginPathIsDirRefusesSymlinks(t *testing.T) {
	cases := map[string]func(t *testing.T, owner, other string){
		"final component to another home": func(t *testing.T, owner, other string) {
			replaceWithSymlink(t, filepath.Join(owner, ".claude", ".credentials.json"), filepath.Join(other, ".claude", ".credentials.json"))
		},
		"intermediate component, relative, to another home": func(t *testing.T, owner, _ string) {
			replaceWithSymlink(t, filepath.Join(owner, ".claude"), "../other/.claude")
		},
		"intermediate component to the host": func(t *testing.T, owner, _ string) {
			replaceWithSymlink(t, filepath.Join(owner, ".claude"), "/etc")
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			manager, owner, other := newLoginHomes(t)
			plant(t, owner, other)
			_, err := manager.LoginPathIsDir("owner", ".claude/.credentials.json")
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("LoginPathIsDir = %v, want a symlink refusal", err)
			}
		})
	}
}

func TestPrepareLoginMountpoint(t *testing.T) {
	manager, _, other := newLoginHomes(t)
	launcher, err := manager.Path("launcher")
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.PrepareLoginMountpoint("launcher", ".claude/.credentials.json", false); err != nil {
		t.Fatalf("prepare file mountpoint: %v", err)
	}
	info, err := os.Lstat(filepath.Join(launcher, ".claude", ".credentials.json"))
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mountpoint = %v, %v; want an empty 0600 regular file", info, err)
	}
	if err := manager.PrepareLoginMountpoint("launcher", ".omp/agent", true); err != nil {
		t.Fatalf("prepare directory mountpoint: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(launcher, ".omp", "agent")); err != nil || !info.IsDir() {
		t.Fatalf("directory mountpoint = %v, %v; want a directory", info, err)
	}

	// The launcher's own login is kept, never truncated or replaced.
	if err := os.WriteFile(filepath.Join(launcher, ".claude", ".credentials.json"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.PrepareLoginMountpoint("launcher", ".claude/.credentials.json", false); err != nil {
		t.Fatalf("prepare over the launcher's own login: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(launcher, ".claude", ".credentials.json")); string(data) != "mine" {
		t.Fatalf("launcher login = %q, want it untouched", data)
	}

	if err := manager.PrepareLoginMountpoint("launcher", ".omp/agent", false); err == nil {
		t.Fatal("a directory was accepted as a file mountpoint")
	}
	if err := manager.PrepareLoginMountpoint("launcher", ".claude/.credentials.json", true); err == nil {
		t.Fatal("a file was accepted as a directory mountpoint")
	}

	// A symlink planted in the launcher's home cannot lead preparation out.
	replaceWithSymlink(t, filepath.Join(launcher, ".claude"), "../other/.codex")
	if err := manager.PrepareLoginMountpoint("launcher", ".claude/.credentials.json", false); err == nil {
		t.Fatal("preparation followed a symlinked parent")
	}
	replaceWithSymlink(t, filepath.Join(launcher, ".omp"), filepath.Join(other, ".omp"))
	if err := manager.PrepareLoginMountpoint("launcher", ".omp/agent/sub", true); err == nil {
		t.Fatal("preparation followed a symlinked parent to another home")
	}
	if _, err := os.Lstat(filepath.Join(other, ".codex")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preparation created %s in another home: %v", filepath.Join(other, ".codex"), err)
	}
	if _, err := os.Lstat(filepath.Join(other, ".omp", "agent", "sub")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preparation created a directory in another home: %v", err)
	}
	replaceWithSymlink(t, filepath.Join(launcher, ".gitconfig"), filepath.Join(other, ".claude", ".credentials.json"))
	if err := manager.PrepareLoginMountpoint("launcher", ".gitconfig", false); err == nil {
		t.Fatal("a symlink was accepted as a file mountpoint")
	}
}

func replaceWithSymlink(t *testing.T, name, target string) {
	t.Helper()
	if err := os.RemoveAll(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

// Two launches that find no state file both succeed: the exclusive create
// lets one through, and the other re-reads what it wrote.
func TestMarkBorrowedStateConcurrentCreate(t *testing.T) {
	t.Parallel()
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const member = domain.MemberID("m1")
	if _, err = m.Path(member); err != nil {
		t.Fatal(err)
	}
	keys := map[string]any{"hasCompletedOnboarding": true}
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = m.MarkBorrowedState(member, ".claude.json", keys)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("mark %d: %v", i, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(m.Root(), string(member), ".claude.json"))
	if err != nil || string(data) != `{"hasCompletedOnboarding":true}` {
		t.Fatalf("state = %q, %v", data, err)
	}
}

func TestLoginFound(t *testing.T) {
	manager, owner, _ := newLoginHomes(t)
	if err := os.WriteFile(filepath.Join(owner, ".codex"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(owner, ".pi", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, ".pi", "agent", "auth.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, ".omp", "agent", "agent.db"), []byte("login"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		rels []string
		want bool
	}{
		{[]string{".claude/.credentials.json"}, true},
		{[]string{".omp/agent"}, true},
		{[]string{".codex/auth.json", ".claude/missing"}, false},
		// An empty file is a mountpoint Aether created, not a login.
		{[]string{".pi/agent/auth.json"}, false},
		{[]string{".claude/missing", ".claude/.credentials.json"}, true},
	} {
		if got, err := manager.LoginFound("owner", tc.rels); err != nil || got != tc.want {
			t.Errorf("LoginFound(%v) = %v, %v; want %v", tc.rels, got, err, tc.want)
		}
	}
	// An empty directory is a mountpoint too.
	if got, err := manager.LoginFound("other", []string{".omp/agent"}); err != nil || got {
		t.Errorf("LoginFound of an empty .omp/agent = %v, %v; want false", got, err)
	}
}
