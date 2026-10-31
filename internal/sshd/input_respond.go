package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func init() {
	registerGuarded(protocol.MethodRunInputRespond, permissions.Steer, runTarget, (*Server).runInputRespond)
}

type controlHeldError struct{ holder domain.MemberID }

func (e *controlHeldError) Error() string { return "session control is held by " + string(e.holder) }

// runInputRespond is run.input.answer for a caller with no ACP stream. It
// answers under the run's control admission lock, so no lease can be taken
// between the check and the answer: with nobody in control, or the caller
// in control from another session, it answers; while another member holds
// control, even in a reconnect window, the request is theirs to answer and
// it refuses rather than displacing them.
func (s *Server) runInputRespond(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunInputRespondParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.RequestID == "" || p.OptionID == "" {
		return nil, invalidParams("request_id and option_id are required")
	}
	if strings.HasPrefix(p.RequestID, domain.SudoRequestPrefix) {
		if perr = s.decideSudo(ctx, member, domain.RunID(p.RunID), p.RequestID, p.OptionID); perr != nil {
			return nil, perr
		}
		return protocol.RunInputRespondResult{Status: protocol.RunInputRespondAnswered}, nil
	}
	run, perr := s.acpRun(ctx, p.RunID)
	if perr != nil {
		return nil, perr
	}
	if perr := s.connectorAnswerRefusal(ctx, run, p.RequestID); perr != nil {
		return nil, perr
	}
	answer := func() error {
		if err := checkSteer(ctx, s.cfg.Store, member, run); err != nil {
			return err
		}
		return s.cfg.Runs.ACPAnswer(run, p.RequestID, p.OptionID, p.Values)
	}
	var err error
	if s.cfg.Control == nil {
		err = answer()
	} else {
		err = s.cfg.Control.AdmitSnapshot(string(run), func(holder control.Snapshot, held bool) error {
			if held && holder.MemberID != member {
				return &controlHeldError{holder: holder.MemberID}
			}
			return answer()
		})
	}
	var heldErr *controlHeldError
	switch {
	case errors.Is(err, acphost.ErrAlreadyAnswered):
		return protocol.RunInputRespondResult{Status: protocol.RunInputRespondAlreadyAnswered}, nil
	case errors.As(err, &heldErr):
		name := string(heldErr.holder)
		if holder, getErr := s.cfg.Store.GetMember(ctx, heldErr.holder); getErr == nil {
			name = holder.DisplayName
		}
		data, _ := json.Marshal(map[string]string{"reason": protocol.ErrorReasonControlHeld, "member_id": string(heldErr.holder)})
		return nil, &protocol.Error{Code: protocol.CodeConflict, Data: data, Message: fmt.Sprintf(
			"%s: %s controls session %s, so the request is theirs to answer; ask them, or take control in the dashboard", protocol.MethodRunInputRespond, name, run)}
	case err != nil:
		return nil, acpError(err)
	}
	return protocol.RunInputRespondResult{Status: protocol.RunInputRespondAnswered}, nil
}

// decideSudo answers a Default run's sudo request whoever holds control.
// Allowing it needs the run's owner or an admin: root in the container acts
// on the owner's home and credentials.
func (s *Server) decideSudo(ctx context.Context, member domain.MemberID, run domain.RunID, request, option string) *protocol.Error {
	if perr := s.connectorAnswerRefusal(ctx, run, request); perr != nil {
		return perr
	}
	if option != domain.SudoAllow && option != domain.SudoDeny {
		return invalidParams(fmt.Sprintf("a sudo request's option is %s or %s, not %q", domain.SudoAllow, domain.SudoDeny, option))
	}
	allow := option == domain.SudoAllow
	if allow {
		// A handoff or a demotion takes this lock, so the check below still
		// holds when the decision lands.
		s.authorizationMu.Lock()
		defer s.authorizationMu.Unlock()
		actor, err := resolveActor(ctx, s.cfg.Store, member)
		if err != nil {
			return rpcError(err)
		}
		target, err := resolveRunTarget(ctx, s.cfg.Store, run)
		if err != nil {
			return rpcError(err)
		}
		if err = permissions.Check(permissions.Sudo, actor, target); err != nil {
			return &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunInputRespond + ": " + err.Error()}
		}
	}
	err := s.cfg.Runs.DecideSudo(ctx, run, request, allow, member)
	if errors.Is(err, scheduler.ErrSudoNotPending) {
		return &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: session %s: %v", protocol.MethodRunInputRespond, run, err)}
	}
	if err != nil {
		return rpcError(err)
	}
	return nil
}
