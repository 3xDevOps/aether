package memberhome

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	claudeLock = LoginLock{Name: ".oauth_refresh.lock"}
	codexLock  = LoginLock{Name: "auth.lock", Flock: true}
)

// Not parallel: it shortens loginLockWait. While one refresh holds a login's
// lock, of either kind, another waits and gives up without refreshing.
func TestRefreshLoginHoldsOneRefresherAtATime(t *testing.T) {
	defer func(wait time.Duration) { loginLockWait = wait }(loginLockWait)
	loginLockWait = 300 * time.Millisecond
	for _, tc := range []struct {
		rel  string
		lock LoginLock
	}{
		{".claude/.credentials.json", claudeLock},
		{".codex/auth.json", codexLock},
	} {
		manager, owner, _ := newLoginHomes(t)
		login := filepath.Join(owner, filepath.FromSlash(tc.rel))
		if err := os.MkdirAll(filepath.Dir(login), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(login, []byte(`{"old":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		holding, release, done := make(chan struct{}), make(chan struct{}), make(chan error)
		go func() {
			done <- manager.RefreshLogin("owner", tc.rel, tc.lock, func([]byte) ([]byte, error) {
				close(holding)
				<-release
				return []byte(`{"first":1}`), nil
			})
		}()
		<-holding
		called := false
		err := manager.RefreshLogin("owner", tc.rel, tc.lock, func([]byte) ([]byte, error) { called = true; return nil, nil })
		if !errors.Is(err, ErrLoginLockHeld) || called {
			t.Fatalf("%s: second RefreshLogin = %v, called %v; want ErrLoginLockHeld before any refresh", tc.rel, err, called)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("%s: first RefreshLogin: %v", tc.rel, err)
		}
		err = manager.RefreshLogin("owner", tc.rel, tc.lock, func(old []byte) ([]byte, error) {
			if string(old) != `{"first":1}` {
				t.Errorf("%s: second refresh read %s, want the first one's login", tc.rel, old)
			}
			return nil, nil
		})
		if err != nil {
			t.Fatalf("%s: RefreshLogin after the release: %v", tc.rel, err)
		}
	}
}

// A login the member replaces with a link during the refresh is refused,
// and the link's target is never written.
func TestRefreshLoginRefusesALinkedLogin(t *testing.T) {
	manager, owner, other := newLoginHomes(t)
	login := filepath.Join(owner, ".claude", ".credentials.json")
	if err := os.WriteFile(login, []byte(`{"old":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(other, "secret")
	if err := os.WriteFile(target, []byte("other's"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := manager.RefreshLogin("owner", ".claude/.credentials.json", claudeLock, func([]byte) ([]byte, error) {
		if err := os.Remove(login); err != nil {
			return nil, err
		}
		if err := os.Symlink(target, login); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		return []byte(`{"new":2}`), nil
	})
	if err == nil {
		t.Fatal("RefreshLogin wrote through a linked login")
	}
	if data, _ := os.ReadFile(target); string(data) != "other's" {
		t.Fatalf("link target = %q, want it untouched", data)
	}
}
