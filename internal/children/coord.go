package children

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Related reports whether a and b are parent and child, either way. Such
// runs may always message each other.
func (s *Service) Related(ctx context.Context, a, b domain.RunID) (bool, error) {
	for _, pair := range [][2]domain.RunID{{a, b}, {b, a}} {
		c, err := s.cfg.Store.GetChild(ctx, pair[0])
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if c.ParentRunID == pair[1] {
			return true, nil
		}
	}
	return false, nil
}

// Lineage is run's parent and its children that have a run, the peers it
// may always message.
func (s *Service) Lineage(ctx context.Context, run domain.RunID) ([]domain.RunID, error) {
	var out []domain.RunID
	if c, err := s.cfg.Store.GetChild(ctx, run); err == nil {
		out = append(out, c.ParentRunID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	kids, err := s.cfg.Store.ListChildren(ctx, run, false)
	if err != nil {
		return nil, err
	}
	for _, c := range kids {
		if c.State == domain.ChildStarted {
			out = append(out, c.RunID)
		}
	}
	return out, nil
}

// ReportRecipient is the parent an attached child's reports go to, or
// nothing.
func (s *Service) ReportRecipient(ctx context.Context, run domain.RunID) (domain.RunID, error) {
	c, err := s.cfg.Store.GetChild(ctx, run)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if c.Detached {
		return "", nil
	}
	return c.ParentRunID, nil
}

// ValidateReport refuses success while the run has unfinished attached
// children.
func (s *Service) ValidateReport(ctx context.Context, run domain.RunID, outcome store.CoordOutcome) error {
	if outcome != store.CoordOutcomeSuccess {
		return nil
	}
	unfinished, _, err := s.cfg.Store.UnfinishedChildren(ctx, run, time.Time{})
	if err != nil {
		return err
	}
	if unfinished == 0 {
		return nil
	}
	return &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
		"coord.report: %d child runs are unfinished (aether-internal child list); wait for their reports and report waiting --for agents, or stop them with child cancel or child detach", unfinished)}
}

// NeedsInput tells an attached child's parent what the child asked its
// owner.
func (s *Service) NeedsInput(ctx context.Context, run domain.RunID, question string) {
	parent, err := s.ReportRecipient(ctx, run)
	if err != nil || parent == "" {
		return
	}
	s.notify(ctx, run, parent, "Child run "+string(run)+" asks its owner: "+question+
		"\nIf you can answer within its task, reply with aether-internal send --to "+string(run)+".", "", "child-needs-input:"+string(run)+":"+digest(question))
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
