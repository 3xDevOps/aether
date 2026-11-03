package memberhome

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/domain"
)

// LoginLock is the lock an agent CLI takes beside its login file before it
// refreshes the login, and rereads the file under.
type LoginLock struct {
	// Name is the lock's name in the login file's directory.
	Name string
	// Flock locks the file Name with flock(2). Otherwise the lock is the
	// directory Name, held while it exists and stale once its mtime is
	// staleLoginLock old, as proper-lockfile keeps it.
	Flock bool
}

// ErrLoginLockHeld means another process held a login's refresh lock for
// all of loginLockWait.
var ErrLoginLockHeld = errors.New("another process holds the login's refresh lock")

const (
	staleLoginLock = 60 * time.Second
	loginLockTouch = 5 * time.Second
	loginLockPoll  = 100 * time.Millisecond
	maxLoginBytes  = 1 << 20
)

var loginLockWait = 10 * time.Second

// ReadLogin returns the login file rel in member's home. It refuses a
// symlink at any component, and a file with another hard link.
func (m *Manager) ReadLogin(member domain.MemberID, rel string) ([]byte, error) {
	home, err := m.openExistingHome(member)
	if err != nil {
		return nil, err
	}
	defer func() { _ = home.Close() }()
	parent, leaf, err := openLoginParent(home, rel)
	if err != nil {
		return nil, fmt.Errorf("memberhome: read login %s in %q: %w", rel, member, err)
	}
	defer func() { _ = parent.Close() }()
	data, err := readLoginFile(parent, leaf)
	if err != nil {
		return nil, fmt.Errorf("memberhome: read login %s in %q: %w", rel, member, err)
	}
	return data, nil
}

// RefreshLogin takes lock beside the login file rel in member's home,
// rereads the file under it, and calls refresh with what it holds. A
// non-nil result is written back on the file's own inode, so it keeps its
// owner and mode and stays the file the member's containers mount; a nil
// one leaves the file alone. refresh's error is returned after the write.
func (m *Manager) RefreshLogin(member domain.MemberID, rel string, lock LoginLock, refresh func(old []byte) ([]byte, error)) error {
	home, err := m.openExistingHome(member)
	if err != nil {
		return err
	}
	defer func() { _ = home.Close() }()
	parent, leaf, err := openLoginParent(home, rel)
	if err != nil {
		return fmt.Errorf("memberhome: refresh login %s in %q: %w", rel, member, err)
	}
	defer func() { _ = parent.Close() }()
	var unlock func()
	if lock.Flock {
		unlock, err = lockFile(home, parent, lock.Name)
	} else {
		unlock, err = lockDir(parent, lock.Name)
	}
	if err != nil {
		return fmt.Errorf("memberhome: lock %s in %q: %w", lock.Name, member, err)
	}
	defer unlock()
	old, err := readLoginFile(parent, leaf)
	if err != nil {
		return fmt.Errorf("memberhome: refresh login %s in %q: %w", rel, member, err)
	}
	rotated, err := refresh(old)
	if rotated != nil {
		if writeErr := writeInPlace(parent, leaf, rotated); writeErr != nil {
			return errors.Join(err, fmt.Errorf("memberhome: write back login %s in %q: %w", rel, member, writeErr))
		}
	}
	return err
}

func readLoginFile(parent *os.Root, leaf string) ([]byte, error) {
	data, err := readRegularFile(parent, leaf, maxLoginBytes)
	if err == nil && data == nil {
		err = fs.ErrNotExist
	}
	return data, err
}

// lockDir holds the directory lock name in dir, touching it so no other
// holder takes it for stale, and removes one that went stale.
func lockDir(dir *os.Root, name string) (func(), error) {
	deadline := time.Now().Add(loginLockWait)
	for {
		err := dir.Mkdir(name, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, err := dir.Lstat(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, err
		case !info.IsDir():
			return nil, fmt.Errorf("%s is not a directory", name)
		case time.Since(info.ModTime()) > staleLoginLock:
			if err := dir.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("remove the stale lock: %w", err)
			}
			continue
		}
		if time.Now().After(deadline) {
			return nil, ErrLoginLockHeld
		}
		time.Sleep(loginLockPoll)
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(loginLockTouch)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				_ = dir.Chtimes(name, now, now)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
		_ = dir.Remove(name)
	}, nil
}

// lockFile holds flock(2) on the file name in dir, creating it owned like
// home when it is missing, as the agent does.
func lockFile(home, dir *os.Root, name string) (func(), error) {
	f, err := dir.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		err = chownFileLikeHome(home, f)
	} else if errors.Is(err, fs.ErrExist) {
		f, err = dir.OpenFile(name, os.O_RDWR, 0)
	}
	if err != nil {
		if f != nil {
			_ = f.Close()
		}
		return nil, err
	}
	deadline := time.Now().Add(loginLockWait)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("flock %s: %w", name, err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrLoginLockHeld
		}
		time.Sleep(loginLockPoll)
	}
}
