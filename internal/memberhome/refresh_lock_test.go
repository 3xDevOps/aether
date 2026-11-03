package memberhome

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var (
	claudeLock = LoginLock{Name: ".oauth_refresh.lock"}
	codexLock  = LoginLock{Name: "auth.lock", Flock: true}
)

func inode(t *testing.T, name string) uint64 {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

// Not parallel: the tests shorten loginLockWait.
func TestRefreshLoginWritesInPlaceUnderTheLock(t *testing.T) {
	manager, owner, _ := newLoginHomes(t)
	login := filepath.Join(owner, ".claude", ".credentials.json")
	if err := os.WriteFile(login, []byte(`{"old":1}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(login, 0o640); err != nil {
		t.Fatal(err)
	}
	before := inode(t, login)
	lockDir := filepath.Join(owner, ".claude", ".oauth_refresh.lock")
	err := manager.RefreshLogin("owner", ".claude/.credentials.json", claudeLock, func(old []byte) ([]byte, error) {
		if string(old) != `{"old":1}` {
			t.Errorf("refresh read %s", old)
		}
		if info, err := os.Stat(lockDir); err != nil || !info.IsDir() {
			t.Errorf("lock while refreshing = %v, %v", info, err)
		}
		return []byte(`{"new":2}`), errors.New("profile check failed")
	})
	if err == nil || err.Error() != "profile check failed" {
		t.Fatalf("RefreshLogin = %v, want refresh's own error", err)
	}
	info, err := os.Stat(login)
	if err != nil || info.Mode() != 0o640 || inode(t, login) != before {
		t.Fatalf("login after refresh = %v, %v; want mode 0640 on inode %d", info, err, before)
	}
	if data, _ := os.ReadFile(login); string(data) != `{"new":2}` {
		t.Fatalf("login = %s, want the rotated one", data)
	}
	if _, err := os.Lstat(lockDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lock after refresh = %v, want removed", err)
	}
	if err := manager.RefreshLogin("owner", ".claude/.credentials.json", claudeLock, func([]byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("RefreshLogin with nothing due: %v", err)
	}
	if data, _ := os.ReadFile(login); string(data) != `{"new":2}` {
		t.Fatalf("login = %s after a refresh that wrote nothing", data)
	}
	if err := manager.RefreshLogin("owner", ".codex/auth.json", codexLock, func([]byte) ([]byte, error) { return nil, nil }); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("RefreshLogin of a missing login = %v, want fs.ErrNotExist", err)
	}
}

func TestRefreshLoginWaitsForAHeldLock(t *testing.T) {
	defer func(wait time.Duration) { loginLockWait = wait }(loginLockWait)
	loginLockWait = 300 * time.Millisecond
	manager, owner, _ := newLoginHomes(t)
	if err := os.MkdirAll(filepath.Join(owner, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, ".codex", "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(owner, ".claude", ".oauth_refresh.lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(owner, ".codex", "auth.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	called := false
	refresh := func([]byte) ([]byte, error) { called = true; return []byte("rotated"), nil }
	for _, tc := range []struct {
		rel  string
		lock LoginLock
	}{
		{".claude/.credentials.json", claudeLock},
		{".codex/auth.json", codexLock},
	} {
		if err := manager.RefreshLogin("owner", tc.rel, tc.lock, refresh); !errors.Is(err, ErrLoginLockHeld) || called {
			t.Fatalf("%s: RefreshLogin = %v, called %v; want ErrLoginLockHeld before any refresh", tc.rel, err, called)
		}
	}
	if _, err := os.Stat(lockDir); err != nil {
		t.Fatalf("held lock = %v, want kept", err)
	}

	stale := time.Now().Add(-2 * staleLoginLock)
	if err := os.Chtimes(lockDir, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshLogin("owner", ".claude/.credentials.json", claudeLock, refresh); err != nil || !called {
		t.Fatalf("RefreshLogin past a stale lock = %v, called %v", err, called)
	}
	_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	called = false
	if err := manager.RefreshLogin("owner", ".codex/auth.json", codexLock, refresh); err != nil || !called {
		t.Fatalf("RefreshLogin after the flock's release = %v, called %v", err, called)
	}
}

func TestReadLoginRefusesASymlink(t *testing.T) {
	manager, owner, other := newLoginHomes(t)
	if err := os.WriteFile(filepath.Join(other, "secret"), []byte("other's"), 0o600); err != nil {
		t.Fatal(err)
	}
	login := filepath.Join(owner, ".claude", ".credentials.json")
	if err := os.Remove(login); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "secret"), login); err != nil {
		t.Fatal(err)
	}
	if data, err := manager.ReadLogin("owner", ".claude/.credentials.json"); err == nil {
		t.Fatalf("ReadLogin through a symlink = %s", data)
	}
}
