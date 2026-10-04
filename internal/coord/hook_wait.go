package coord

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// WakeAdmission must call dispatch only while holding the shared run-control
// admission boundary, after freshly checking run and mission authority. It is
// never called during the observer wait. A nil seam fails closed.
type WakeAdmission func(ctx context.Context, run domain.RunID, dispatch func() error) error

var errWakeSuppressed = errors.New("coord: native wake suppressed")

type hookWaiter struct {
	ch       chan struct{}
	signaled bool
	consumer bool
}

type hookObservation struct {
	status protocol.CoordStatusResult
	waiter *hookWaiter
}

func validateHookStatus(p protocol.CoordHookStatusParams) *protocol.Error {
	const method = protocol.MethodCoordHookStatus
	if p.WaitSeconds < 0 || p.WaitSeconds > protocol.CoordMaxInboxWaitSeconds {
		return invalidParams(method, fmt.Sprintf("wait_seconds must be between 0 and %d", protocol.CoordMaxInboxWaitSeconds))
	}
	if len(p.SeenMessageIDs) > protocol.CoordMaxUnread {
		return invalidParams(method, "too many seen_message_ids")
	}
	for _, id := range p.SeenMessageIDs {
		if !protocol.ValidCoordMessageID(id) {
			return invalidParams(method, "seen_message_ids contains an invalid message ID")
		}
	}
	return nil
}

func sameMessageIDs(current []string, seen map[string]struct{}) bool {
	if len(current) != len(seen) {
		return false
	}
	for _, id := range current {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

// commandHookStatus adds the unread message IDs to status without waiting or
// admitting a wake, so a command hook can tell new mail from mail it already
// announced even when the unread count is unchanged.
func (s *Service) commandHookStatus(ctx context.Context, run domain.RunID) (protocol.CoordStatusResult, *protocol.Error) {
	status, rpcErr := s.Status(ctx, run)
	if rpcErr != nil {
		return status, rpcErr
	}
	mail, supported := s.cfg.Mail.(store.UnackedRunMessageIDsStore)
	if !supported || status.Unread == 0 {
		return status, nil
	}
	ids, err := mail.ListUnackedRunMessageIDs(ctx, run, protocol.CoordMaxUnread)
	if err != nil {
		return status, internalError(protocol.MethodCoordHookStatus, err)
	}
	status.UnreadMessageIDs = ids
	return status, nil
}

// observeHook is called with a run reference held through the eventual frame
// write. Its waiter also stays registered through admission, so an Inbox
// consumer that wins the append race cannot lose priority as it returns.
func (s *Service) observeHook(ctx context.Context, run domain.RunID, p protocol.CoordHookStatusParams) (hookObservation, *protocol.Error) {
	const method = protocol.MethodCoordHookStatus
	if s.cfg.Disabled {
		return hookObservation{}, unavailable(method)
	}
	if err := validateHookStatus(p); err != nil {
		return hookObservation{}, err
	}
	mail, supported := s.cfg.Mail.(store.UnackedRunMessageIDsStore)
	if !supported {
		status, err := s.Status(ctx, run)
		return hookObservation{status: status}, err
	}
	waiter := s.registerHookWaiter(run)
	observation := hookObservation{waiter: waiter}
	seen := make(map[string]struct{}, len(p.SeenMessageIDs))
	for _, id := range p.SeenMessageIDs {
		seen[id] = struct{}{}
	}
	ids, err := mail.ListUnackedRunMessageIDs(ctx, run, protocol.CoordMaxUnread)
	if err != nil {
		return observation, internalError(method, err)
	}
	if p.WaitSeconds > 0 && sameMessageIDs(ids, seen) {
		timer := time.NewTimer(time.Duration(p.WaitSeconds) * time.Second)
		defer timer.Stop()
		select {
		case <-waiter.ch:
		case <-timer.C:
		case <-ctx.Done():
		case <-s.serveCtx.Done():
		}
	}
	if s.isRunClosing(run) || ctx.Err() != nil || s.serveCtx.Err() != nil {
		return observation, runClosing(method)
	}
	// Re-snapshot even after timeout: unchanged mail may now be eligible after
	// protection/takeover release. This never advances delivery or ack state.
	ids, err = mail.ListUnackedRunMessageIDs(ctx, run, protocol.CoordMaxUnread)
	if err != nil {
		return observation, internalError(method, err)
	}
	status, rpcErr := s.Status(ctx, run)
	if rpcErr != nil {
		return observation, rpcErr
	}
	status.WaitSupported = true
	status.UnreadMessageIDs = ids
	status.Unread = len(ids)
	observation.status = status
	return observation, nil
}

func (s *Service) registerHookWaiter(run domain.RunID) *hookWaiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiter := &hookWaiter{ch: make(chan struct{}), consumer: s.inboxConsumers[run] > 0}
	state := s.runs[run]
	if s.closed || (state != nil && state.closing) {
		waiter.signal()
		return waiter
	}
	if s.hookWaiters[run] == nil {
		s.hookWaiters[run] = make(map[*hookWaiter]struct{})
	}
	s.hookWaiters[run][waiter] = struct{}{}
	return waiter
}

func (s *Service) releaseHookWaiter(run domain.RunID, waiter *hookWaiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.hookWaiters[run], waiter)
	if len(s.hookWaiters[run]) == 0 {
		delete(s.hookWaiters, run)
	}
}

func (w *hookWaiter) signal() {
	if !w.signaled {
		close(w.ch)
		w.signaled = true
	}
}

func (s *Service) cancelHookWaitersLocked(run domain.RunID) {
	for waiter := range s.hookWaiters[run] {
		waiter.signal()
	}
}

func (s *Service) hookConsumerWon(run domain.RunID, waiter *hookWaiter) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return waiter == nil || waiter.consumer || s.inboxConsumers[run] > 0
}
