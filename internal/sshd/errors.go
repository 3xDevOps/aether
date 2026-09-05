package sshd

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

// The sibling packages' exported sentinels, matched via errors.Is when
// mapping seam errors to wire codes. Wave 1's local copies were
// reconciled with the real values at integration.
var (
	errInvalidTransition = scheduler.ErrInvalidTransition
	errDiskFull          = scheduler.ErrDiskFull
	errNoSession         = ptyhost.ErrNoSession
	errSessionEnded      = ptyhost.ErrSessionEnded
	errWriteDenied       = ptyhost.ErrWriteDenied
)

// errMemberRemoved is returned when the authenticated member has been
// deleted from the store since the handshake; it maps to CodeDenied.
var errMemberRemoved = errors.New("sshd: member no longer exists")

// errMemberPending is returned when the authenticated member is still
// awaiting admin approval; it maps to CodeDenied.
var errMemberPending = errors.New("sshd: membership pending admin approval")

// memberFor re-fetches the authenticated member, mapping a deleted row to
// errMemberRemoved.
func (s *Server) memberFor(ctx context.Context, member domain.MemberID) (*domain.Member, error) {
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errMemberRemoved
		}
		return nil, err
	}
	return m, nil
}

// checkMember re-validates that the authenticated member still exists and
// is approved, so deleting a member revokes access for established
// connections too - not only for future handshakes - and a pending member
// can do nothing until approved (the control channel gates per method
// instead, so server.info stays reachable).
func (s *Server) checkMember(ctx context.Context, member domain.MemberID) error {
	m, err := s.memberFor(ctx, member)
	if err != nil {
		return err
	}
	if m.Pending {
		return errMemberPending
	}
	return nil
}

// rpcError maps an error from the store or a seam call to the wire error
// object per the contract's error-mapping table.
func rpcError(err error) *protocol.Error {
	code := protocol.CodeInternal
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = protocol.CodeNotFound
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrInUse),
		errors.Is(err, scheduler.ErrRunShellTabLimit):
		code = protocol.CodeConflict
	case errors.Is(err, scheduler.ErrInvalidRunShellTab), errors.Is(err, scheduler.ErrInvalidTerminalTab):
		code = protocol.CodeInvalidParams
	case errors.Is(err, errInvalidTransition), errors.Is(err, scheduler.ErrTerminalTabLimit),
		errors.Is(err, scheduler.ErrTerminalNotRunning):
		code = protocol.CodeInvalidState
	case errors.Is(err, errWriteDenied), errors.Is(err, errMemberRemoved),
		errors.Is(err, errMemberPending), errors.Is(err, permissions.ErrDenied):
		code = protocol.CodeDenied
	case errors.Is(err, errNoSession), errors.Is(err, errSessionEnded), errors.Is(err, errDiskFull):
		// The free-space floor is a "not right now", not a bad request: the
		// call is well-formed and becomes possible again once the disk does.
		code = protocol.CodeUnavailable
	}
	return &protocol.Error{Code: code, Message: err.Error()}
}
