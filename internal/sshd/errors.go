package sshd

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

var (
	errInvalidTransition = scheduler.ErrInvalidTransition
	errDiskFull          = scheduler.ErrDiskFull
	errNoSession         = ptyhost.ErrNoSession
	errSessionEnded      = ptyhost.ErrSessionEnded
	errWriteDenied       = ptyhost.ErrWriteDenied
)

// errMemberRemoved means the member was deleted since the handshake.
var errMemberRemoved = errors.New("sshd: member no longer exists")

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

// checkMember re-validates the member on every call so deleting a member
// revokes established connections too. The control channel gates pending
// members per method instead, so server.info stays reachable.
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

// rpcError maps a store or seam error to its wire error. git's raw transport
// output never crosses this boundary.
func rpcError(err error) *protocol.Error {
	var typed *protocol.Error
	if errors.As(err, &typed) && typed != nil {
		return typed
	}
	code := protocol.CodeInternal
	switch mirrorKind(err) {
	case gitengine.MirrorErrorAuthFailed:
		code = protocol.CodeDenied
	case gitengine.MirrorErrorOffline, gitengine.MirrorErrorUnsupported:
		code = protocol.CodeUnavailable
	case gitengine.MirrorErrorSourceMissing:
		code = protocol.CodeNotFound
	case gitengine.MirrorErrorRewritten, gitengine.MirrorErrorDiverged, gitengine.MirrorErrorCASConflict:
		code = protocol.CodeConflict
	case gitengine.MirrorErrorInvalidRequest:
		code = protocol.CodeInvalidParams
	case gitengine.MirrorErrorNotConfigured, gitengine.MirrorErrorNoCandidate:
		code = protocol.CodeInvalidState
	case gitengine.MirrorErrorFailed:
		code = protocol.CodeInternal
	default:
		switch {
		case errors.Is(err, store.ErrNotFound):
			code = protocol.CodeNotFound
		case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrInUse),
			errors.Is(err, store.ErrMissionIdempotencyConflict),
			errors.Is(err, scheduler.ErrRunShellTabLimit), errors.Is(err, scheduler.ErrAgentInstallRunning),
			errors.Is(err, ptyhost.ErrSessionReplaced):
			// Matches coord.missionRPCError for a reused mission idempotency key.
			code = protocol.CodeConflict
		case errors.Is(err, scheduler.ErrInvalidRunShellTab), errors.Is(err, scheduler.ErrInvalidTerminalTab),
			errors.Is(err, store.ErrInvalidCursor):
			code = protocol.CodeInvalidParams
		case errors.Is(err, errInvalidTransition), errors.Is(err, scheduler.ErrTerminalTabLimit),
			errors.Is(err, scheduler.ErrTerminalNotRunning), errors.Is(err, scheduler.ErrGitHubNotLoggedIn),
			errors.Is(err, scheduler.ErrGitHubScopeMissing), errors.Is(err, scheduler.ErrGitHubCLIMissing),
			errors.Is(err, scheduler.ErrGitHubCLIBroken), errors.Is(err, scheduler.ErrGitHubCLIOutdated),
			errors.Is(err, store.ErrMissionPhase), errors.Is(err, scheduler.ErrNoLiveEnvironment):
			code = protocol.CodeInvalidState
		case errors.Is(err, errWriteDenied), errors.Is(err, errMemberRemoved),
			errors.Is(err, errMemberPending), errors.Is(err, permissions.ErrDenied):
			code = protocol.CodeDenied
		case errors.Is(err, errNoSession), errors.Is(err, errSessionEnded), errors.Is(err, errDiskFull),
			errors.Is(err, scheduler.ErrMemoryPressure), errors.Is(err, scheduler.ErrCapacityUnknown),
			errors.Is(err, scheduler.ErrBrowserCPUCapacity):
			// Capacity refusals are retryable availability failures, not bad requests.
			code = protocol.CodeUnavailable
		}
	}
	out := &protocol.Error{Code: code, Message: err.Error()}
	if failure := mirrorFailure(err); failure != nil {
		if data, marshalErr := json.Marshal(failure); marshalErr == nil {
			out.Data = data
		}
	}
	return out
}

func mirrorKind(err error) gitengine.MirrorErrorKind {
	var mirrorErr *gitengine.MirrorError
	if errors.As(err, &mirrorErr) && mirrorErr != nil && mirrorErr.Kind != "" {
		return mirrorErr.Kind
	}
	return ""
}

func mirrorFailure(err error) *protocol.MirrorFailure {
	var captureErr *scheduler.BaseCaptureError
	if !errors.As(err, &captureErr) || captureErr == nil {
		return nil
	}
	failure := &protocol.MirrorFailure{
		AcceptedCommit: captureErr.Capture.Commit,
		Source:         captureErr.Capture.Source,
		Branch:         captureErr.Capture.Branch,
	}
	var mirrorErr *gitengine.MirrorError
	if errors.As(captureErr.Err, &mirrorErr) && mirrorErr != nil {
		failure.Kind = string(mirrorErr.Kind)
		failure.BaseCommit = mirrorErr.Base
		failure.ObservedCommit = mirrorErr.Observed
		if failure.Source == "" {
			failure.Source = mirrorErr.SourceURL
		}
		if failure.Branch == "" {
			failure.Branch = mirrorErr.Branch
		}
	}
	if failure.Kind == "" {
		failure.Kind = string(gitengine.MirrorErrorFailed)
	}
	if failure.AcceptedCommit == "" && failure.BaseCommit == "" && failure.ObservedCommit == "" &&
		failure.Source == "" && failure.Branch == "" {
		return nil
	}
	return failure
}
