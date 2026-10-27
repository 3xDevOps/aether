package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	_, err := s.NotifyRun(ctx, from, to, body, correlation, key)
	return err
}

// NotifyRun is Notify reporting whether the message is in the recipient's
// inbox: false for a run that ended, which gets nothing. Each line of body
// is kept.
func (s *Service) NotifyRun(ctx context.Context, from, to domain.RunID, body, correlation, key string) (bool, error) {
	target, err := s.cfg.Store.GetRun(ctx, to)
	if err != nil {
		return false, fmt.Errorf("notify run %s: %w", to, err)
	}
	if target.Status.Terminal() {
		return false, nil
	}
	msg := &store.RunMessage{
		WorkspaceID: target.WorkspaceID, FromRun: from, ToRun: to, Kind: store.RunMessageKindMessage,
		Body: cleanNoticeText(body, protocol.CoordMaxBodyBytes), CorrelationID: correlation, IdempotencyKey: key,
	}
	created, err := appendRunMessage(ctx, s.cfg.Mail, msg, false)
	if err != nil {
		return false, fmt.Errorf("notify run %s: %w", to, err)
	}
	if created {
		s.wakeInbox(to)
	}
	return true, nil
}

// cleanNoticeText is cleanCoordText line by line, so a server-written
// message keeps its lines.
func cleanNoticeText(text string, maxBytes int) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = cleanCoordText(line, maxBytes)
	}
	text = strings.Join(lines, "\n")
	for len(text) > maxBytes {
		runes := []rune(text)
		text = string(runes[:len(runes)-1])
	}
	return text
}

func isChildMethod(method string) bool {
	switch method {
	case protocol.MethodChildOptions, protocol.MethodChildStart, protocol.MethodChildList,
		protocol.MethodChildCancel, protocol.MethodChildDetach, protocol.MethodChildFetch:
		return true
	}
	return false
}
