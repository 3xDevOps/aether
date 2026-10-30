package coord

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const provisionalHeader = "Provisional report: this run ended its turn without `aether-internal report`. " +
	"A report from it supersedes this one. Its last message:\n\n"

// ProvisionalReport forwards a swarm worker's or child run's last message to
// its integrator or parent
// when the turn that started at since ended without a coord.report. The turn
// start keys the message, so each turn sends at most one.
func (s *Service) ProvisionalReport(ctx context.Context, run domain.RunID, since time.Time, message string) error {
	if s.cfg.Disabled {
		return nil
	}
	var recipient domain.RunID
	var err error
	if s.cfg.Mission != nil {
		recipient, err = s.cfg.Mission.ReportRecipient(ctx, run)
	}
	if err == nil && recipient == "" && s.cfg.Children != nil {
		recipient, err = s.cfg.Children.ReportRecipient(ctx, run)
	}
	if err != nil {
		return fmt.Errorf("provisional report recipient: %w", err)
	}
	if recipient == "" {
		return nil
	}
	target, err := s.cfg.Store.GetRun(ctx, recipient)
	if err != nil {
		return fmt.Errorf("provisional report integrator: %w", err)
	}
	// Under the report lock a report either is already reserved or forwards
	// after this message and retires it.
	lock := s.reportLock(run)
	lock.Lock()
	defer lock.Unlock()
	reported, err := s.cfg.Mail.CoordReportedSince(ctx, run, since)
	if err != nil || reported {
		return err
	}
	if message == "" {
		message = "(none)"
	}
	if room := protocol.CoordMaxBodyBytes - len(provisionalHeader); len(message) > room {
		message = strings.ToValidUTF8(message[:room], "")
	}
	turn := strconv.FormatInt(since.UnixMicro(), 10)
	created, err := appendRunMessage(ctx, s.cfg.Mail, &store.RunMessage{
		WorkspaceID: target.WorkspaceID, FromRun: run, ToRun: target.ID,
		Kind: store.RunMessageKindMessage, Body: provisionalHeader + message,
		CorrelationID:  store.ProvisionalReportPrefix + turn,
		IdempotencyKey: "coord-provisional:" + turn + ":" + string(target.ID),
	}, false)
	if err != nil {
		return fmt.Errorf("enqueue provisional report: %w", err)
	}
	if created {
		s.wakeInbox(target.ID)
	}
	return nil
}
