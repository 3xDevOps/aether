package ptyhost

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SessionAdmission identifies the resource incarnation independently of its
// controller generation. Admit must synchronously invoke accept exactly once
// under the caller's authorization lock, keeping takeover fenced through the
// physical effect. It must not re-enter this session or return an error after
// accepting. A missing callback never grants authority.
type SessionAdmission struct {
	Generation uint64
	Member domain.MemberID
	Admit func(accept func() error) error
}

func (h *Host) admitSession(ctx context.Context, key SessionKey, a SessionAdmission, effect func(*session) error) error {
	if a.Generation == 0 || a.Admit == nil { return ErrWriteDenied }
	if err := ctx.Err(); err != nil { return err }
	s := h.lookup(key)
	if s == nil { return ErrNoSession }
	if s.generation != a.Generation { return ErrSessionReplaced }
	if h.cfg.Gate != nil {
		if err := h.cfg.Gate(ctx, a.Member, key); err != nil { return fmt.Errorf("%w: %v", ErrWriteDenied, err) }
	}
	var mu sync.Mutex
	called, closed := false, false
	var effectErr error
	admissionErr := a.Admit(func() error {
		mu.Lock()
		defer mu.Unlock()
		if closed || called { return errors.New("ptyhost: session admission repeated or deferred") }
		called = true
		if err := ctx.Err(); err != nil { effectErr = err; return err }
		if h.lookup(key) != s { effectErr = ErrSessionReplaced; return effectErr }
		effectErr = effect(s)
		return effectErr
	})
	mu.Lock()
	defer mu.Unlock()
	closed = true
	if admissionErr != nil { return admissionErr }
	if !called { return ErrWriteDenied }
	return effectErr
}

// WriteSessionInput preserves the host gate and serialized physical write lane.
// Callers encode semantic input against an observation's modes; these bytes are
// never classified as terminal replies or filtered heuristically.
func (h *Host) WriteSessionInput(ctx context.Context, key SessionKey, admission SessionAdmission, p []byte) error {
	if len(p) > MaxOutputBytes { return errors.New("ptyhost: input limit out of bounds") }
	return h.admitSession(ctx, key, admission, func(s *session) error {
		return s.writeStdinContext(ctx, p)
	})
}

// ResizeSession completes the runtime resize inside admission. Like existing
// attach resizes it commits geometry before output emitted during the RPC and
// notifies all viewers in that same order. No synthetic writable client is
// installed, and read-only development viewers contribute no geometry.
func (h *Host) ResizeSession(ctx context.Context, key SessionKey, admission SessionAdmission, cols, rows uint) error {
	if err := validateScreenDimensions(cols, rows); err != nil { return err }
	return h.admitSession(ctx, key, admission, func(s *session) error {
		for {
			if err := ctx.Err(); err != nil { return err }
			s.mu.Lock()
			if s.stopped || s.ended {
				s.mu.Unlock()
				return ErrSessionEnded
			}
			if done := s.resizeDone; done != nil {
				s.mu.Unlock()
				select { case <-done: continue; case <-ctx.Done(): return ctx.Err() }
			}
			if s.acceptedCols == cols && s.acceptedRows == rows {
				s.mu.Unlock()
				return nil
			}
			s.cols, s.rows = cols, rows
			s.geoGen++
			s.resizeDone = make(chan struct{})
			s.mu.Unlock()
			return s.applyResizeContext(ctx)
		}
	})
}
