package coord

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// ChildService is the optional child-run authority behind the run socket.
// It owns the child methods and the parent-child relation; the transport
// binds identity.
type ChildService interface {
	Capabilities(ctx context.Context, run domain.RunID) ([]string, error)
	HandleAgent(ctx context.Context, run domain.RunID, method string, params json.RawMessage) (any, error)
	// Related reports whether two runs are parent and child.
	Related(ctx context.Context, a, b domain.RunID) (bool, error)
	// Lineage is the run's parent and started children.
	Lineage(ctx context.Context, run domain.RunID) ([]domain.RunID, error)
	ReportRecipient(ctx context.Context, run domain.RunID) (domain.RunID, error)
	ValidateReport(ctx context.Context, run domain.RunID, outcome store.CoordOutcome) error
	NeedsInput(ctx context.Context, run domain.RunID, question string)
}

func (s *Service) handleChild(ctx context.Context, run domain.RunID, method string, params json.RawMessage) (any, *protocol.Error) {
	if s.cfg.Disabled {
		return nil, unavailable(method)
	}
	if s.cfg.Children == nil {
		return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + method}
	}
	if !s.enterRun(run) {
		return nil, runClosing(method)
	}
	defer s.leaveRun(run)
	if method == protocol.MethodChildStart {
		lock := s.reportLock(run)
		lock.Lock()
		defer lock.Unlock()
	}
	result, err := s.cfg.Children.HandleAgent(ctx, run, method, params)
	return result, missionRPCError(method, err)
}

// Notify appends a server-written message from one run to another, which
// no route authorizes, and wakes the recipient. A retry with the same key
// adds nothing.
func (s *Service) Notify(ctx context.Context, from, to domain.RunID, body, correlation, key string) error {
	target, err := s.cfg.Store.GetRun(ctx, to)
	if err != nil {
		return fmt.Errorf("notify run %s: %w", to, err)
	}
	if target.Status.Terminal() {
		return nil
	}
	msg := &store.RunMessage{
		WorkspaceID: target.WorkspaceID, FromRun: from, ToRun: to, Kind: store.RunMessageKindMessage,
		Body: cleanCoordText(body, protocol.CoordMaxBodyBytes), CorrelationID: correlation, IdempotencyKey: key,
	}
	created, err := appendRunMessage(ctx, s.cfg.Mail, msg)
	if err != nil {
		return fmt.Errorf("notify run %s: %w", to, err)
	}
	if created {
		s.wakeInbox(to)
	}
	return nil
}

func isChildMethod(method string) bool {
	switch method {
	case protocol.MethodChildOptions, protocol.MethodChildStart, protocol.MethodChildList,
		protocol.MethodChildCancel, protocol.MethodChildFetch:
		return true
	}
	return false
}
