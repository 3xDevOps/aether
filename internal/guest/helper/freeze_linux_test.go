package helper

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeFS struct {
	mu     sync.Mutex
	frozen bool
	calls  []bool
	fail   bool
}

func (f *fakeFS) set(frozen bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("EINVAL")
	}
	if frozen == f.frozen {
		return errors.New("EBUSY: already in that state")
	}
	f.frozen = frozen
	f.calls = append(f.calls, frozen)
	return nil
}

func (f *fakeFS) state() (bool, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frozen, len(f.calls)
}

func TestFreezeThawsAtItsDeadline(t *testing.T) {
	fs := &fakeFS{}
	f := &freezer{set: fs.set}
	if err := f.freeze("a", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if frozen, _ := fs.state(); !frozen {
		t.Fatal("not frozen")
	}
	deadline := time.Now().Add(5 * time.Second)
	for frozen, _ := fs.state(); frozen; frozen, _ = fs.state() {
		if time.Now().After(deadline) {
			t.Fatal("the deadline did not thaw the filesystem")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if frozen, err := f.thaw("a"); err != nil || frozen {
		t.Fatalf("a thaw after the deadline = %v, %v; it must report the freeze did not hold", frozen, err)
	}
}

func TestFreezeAgainMovesTheDeadline(t *testing.T) {
	fs := &fakeFS{}
	f := &freezer{set: fs.set}
	if err := f.freeze("a", 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := f.freeze("b", time.Hour); err != nil {
		t.Fatalf("second freeze: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if frozen, calls := fs.state(); !frozen || calls != 1 {
		t.Fatalf("frozen %v after %d calls; the first deadline must not thaw the second freeze", frozen, calls)
	}
	if frozen, err := f.thaw("a"); err != nil || frozen {
		t.Fatalf("a thaw of the replaced freeze = %v, %v", frozen, err)
	}
	if frozen, calls := fs.state(); !frozen || calls != 1 {
		t.Fatal("a thaw of the replaced freeze thawed the current one")
	}
	if frozen, err := f.thaw("b"); err != nil || !frozen {
		t.Fatalf("thaw = %v, %v", frozen, err)
	}
	if frozen, err := f.thaw("b"); err != nil || !frozen {
		t.Fatalf("a retried thaw = %v, %v; it must get the first one's answer", frozen, err)
	}
	if frozen, calls := fs.state(); frozen || calls != 2 {
		t.Fatalf("frozen %v after %d calls", frozen, calls)
	}
}

func TestFailedThawKeepsTheDeadline(t *testing.T) {
	fs := &fakeFS{}
	f := &freezer{set: fs.set}
	if err := f.freeze("a", 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	fs.fail = true
	fs.mu.Unlock()
	if _, err := f.thaw("a"); err == nil {
		t.Fatal("thaw succeeded")
	}
	fs.mu.Lock()
	fs.fail = false
	fs.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for frozen, _ := fs.state(); frozen; frozen, _ = fs.state() {
		if time.Now().After(deadline) {
			t.Fatal("a failed thaw dropped the deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
