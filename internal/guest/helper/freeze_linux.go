package helper

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxFreeze bounds a freeze, so a server that dies while the root disk is
// frozen leaves the session writable again.
const maxFreeze = 10 * time.Minute

// The FIFREEZE and FITHAW ioctls of linux/fs.h, the same on every
// architecture; golang.org/x/sys/unix does not define them.
const (
	fiFreeze = 0xc0045877
	fiThaw   = 0xc0045878
)

// freezer holds the root filesystem frozen until a thaw or its deadline.
// It remembers how the last freeze ended, so a Thaw retried after a lost
// reply gets the answer the first one had.
type freezer struct {
	set func(frozen bool) error

	mu    sync.Mutex
	timer *time.Timer
	// deadlines counts the timers set; each knows its own number.
	deadlines uint64
	id        string
	// lastID's freeze held until its thaw when lastHeld; its deadline ended
	// it otherwise.
	lastID   string
	lastHeld bool
}

func freezeRoot(frozen bool) error {
	root, err := os.Open("/")
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	req := uint(fiThaw)
	if frozen {
		req = fiFreeze
	}
	return unix.IoctlSetInt(int(root.Fd()), req, 0)
}

func (f *freezer) freeze(id string, timeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timer != nil {
		f.timer.Stop()
	} else if err := f.set(true); err != nil {
		return err
	}
	f.deadlines++
	deadline := f.deadlines
	f.timer, f.id = time.AfterFunc(timeout, func() { f.thawAtDeadline(deadline) }), id
	return nil
}

// thawAtDeadline thaws only the freeze its deadline was set for: a later
// Freeze replaced it.
func (f *freezer) thawAtDeadline(deadline uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timer != nil && f.deadlines == deadline && f.thawLocked() == nil {
		f.lastHeld = false
	}
}

// thaw ends freeze id and reports whether it held until now; another
// freeze is left as it is.
func (f *freezer) thaw(id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timer == nil || f.id != id {
		return f.lastID == id && f.lastHeld, nil
	}
	if err := f.thawLocked(); err != nil {
		return false, err
	}
	f.lastHeld = true
	return true, nil
}

// thawAny ends whatever freeze there is.
func (f *freezer) thawAny() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timer == nil {
		return nil
	}
	err := f.thawLocked()
	if err == nil {
		f.lastHeld = false
	}
	return err
}

func (f *freezer) thawLocked() error {
	if err := f.set(false); err != nil {
		return err
	}
	f.timer.Stop()
	f.timer, f.lastID = nil, f.id
	return nil
}

func (s *service) Freeze(_ context.Context, req *guestpb.FreezeRequest) (*guestpb.FreezeReply, error) {
	if s.freezer == nil {
		return nil, status.Error(codes.FailedPrecondition, "the helper is not PID 1")
	}
	timeout := time.Duration(req.GetTimeoutMs()) * time.Millisecond
	if timeout <= 0 || timeout > maxFreeze {
		return nil, status.Errorf(codes.InvalidArgument, "freeze timeout must be between 1 ms and %s", maxFreeze)
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "a freeze needs an id")
	}
	if err := s.freezer.freeze(req.GetId(), timeout); err != nil {
		return nil, status.Errorf(codes.Internal, "freeze the root filesystem: %v", err)
	}
	return &guestpb.FreezeReply{}, nil
}

func (s *service) Thaw(_ context.Context, req *guestpb.ThawRequest) (*guestpb.ThawReply, error) {
	if s.freezer == nil {
		return nil, status.Error(codes.FailedPrecondition, "the helper is not PID 1")
	}
	frozen, err := s.freezer.thaw(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "thaw the root filesystem: %v", err)
	}
	return &guestpb.ThawReply{Frozen: frozen}, nil
}
