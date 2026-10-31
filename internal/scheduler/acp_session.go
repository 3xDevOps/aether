package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

var ErrACPItemNotFound = errors.New("scheduler: no such conversation item")

var errNoItemLog = errors.New("scheduler: the session has no conversation item log")

type ACPStream struct {
	Replay []acphost.Item
	// Reset means the cursor is outside retained history: Replay contains
	// only real retained items and the viewer drops its previous projection.
	Reset           bool
	TruncatedBefore bool
	Epoch           int64
	Seq             int64
	// OldestSeq is the first replayed seq when the replay does not continue
	// from the viewer's cursor; the viewer pages older items from it.
	OldestSeq int64
	// Items carries every later item while the session is live. It is nil
	// when no session is live, and closed when the session ends or the
	// viewer falls too far behind; either way the viewer resubscribes.
	Items <-chan acphost.Item
	// Started is closed when the viewer should resubscribe, for a stream
	// that opened with no live session: the next session started, or the
	// closing session it found has ended.
	Started <-chan struct{}
	State   *acphost.State
	Cancel  func()
}

func (s *Scheduler) ACPSubscribe(run domain.RunID, afterSeq int64) (ACPStream, error) {
	sess, started, release := s.acp.started(run)
	if sess != nil {
		replay, items, cancel, err := sess.Subscribe(afterSeq)
		if err == nil {
			state := sess.State()
			out := ACPStream{Replay: replay.Items, Reset: replay.Reset, TruncatedBefore: replay.TruncatedBefore, Items: items, State: &state, Cancel: cancel, Seq: replay.Seq, Epoch: replay.Epoch, OldestSeq: replay.OldestSeq}
			return out, nil
		}
		if !errors.Is(err, acphost.ErrClosed) {
			return ACPStream{}, err
		}
		// The session is closing: the viewer resubscribes once it is gone.
		started = sess.Done()
	}
	out := ACPStream{Started: started, Cancel: release}
	log, err := s.openItemLog(run)
	if err != nil {
		release()
		return ACPStream{}, err
	}
	if log == nil {
		return out, nil
	}
	defer func() { _ = log.Close() }()
	replay, err := log.Replay(afterSeq)
	if err != nil {
		release()
		return ACPStream{}, err
	}
	out.Replay, out.Reset, out.TruncatedBefore = replay.Items, replay.Reset, replay.TruncatedBefore
	out.Epoch, out.Seq, out.OldestSeq = replay.Epoch, replay.Seq, replay.OldestSeq
	return out, nil
}

func (s *Scheduler) ACPPending(run domain.RunID) []acphost.Request {
	sess := s.acp.session(run)
	if sess == nil {
		return nil
	}
	return sess.State().Pending
}

func (s *Scheduler) ACPHistory(run domain.RunID, beforeSeq int64, limit int) (acphost.HistoryPage, error) {
	if beforeSeq <= 0 {
		beforeSeq = math.MaxInt64
	}
	var page acphost.HistoryPage
	err := s.withItemLog(run, func(log *acphost.Log) (err error) {
		page, err = log.History(beforeSeq, limit)
		return err
	})
	if errors.Is(err, errNoItemLog) {
		return acphost.HistoryPage{}, nil
	}
	return page, err
}

func (s *Scheduler) ACPItem(run domain.RunID, seq int64) (acphost.Item, error) {
	var item acphost.Item
	err := s.withItemLog(run, func(log *acphost.Log) error {
		var found bool
		var err error
		item, found, err = log.Item(seq)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: %d", ErrACPItemNotFound, seq)
		}
		return nil
	})
	if errors.Is(err, errNoItemLog) {
		err = fmt.Errorf("%w: %d", ErrACPItemNotFound, seq)
	}
	return item, err
}

func (s *Scheduler) withItemLog(run domain.RunID, read func(*acphost.Log) error) error {
	if sess := s.acp.session(run); sess != nil {
		if err := read(sess.Log()); !errors.Is(err, acphost.ErrLogClosed) {
			return err
		}
	}
	log, err := s.openItemLog(run)
	if err != nil {
		return err
	}
	if log == nil {
		return errNoItemLog
	}
	defer func() { _ = log.Close() }()
	return read(log)
}

func (s *Scheduler) recordedMode(run domain.RunID) string {
	log, err := s.openItemLog(run)
	if err != nil || log == nil {
		return ""
	}
	defer func() { _ = log.Close() }()
	return log.Mode()
}

func (s *Scheduler) openItemLog(run domain.RunID) (*acphost.Log, error) {
	log, err := s.itemLogs.Open(s.cfg.PTY.ItemLogPath(run))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return log, err
}

func (s *Scheduler) ACPAnswer(run domain.RunID, requestID, optionID string, values map[string]any) error {
	sess, err := s.acp.live(context.Background(), run)
	if err != nil {
		return err
	}
	return sess.Answer(requestID, optionID, values)
}

func (s *Scheduler) ACPCancel(ctx context.Context, run domain.RunID) error {
	sess, err := s.acp.live(ctx, run)
	if err != nil {
		return err
	}
	return sess.Cancel(ctx)
}

func (s *Scheduler) ACPSetOption(ctx context.Context, run domain.RunID, optionID string, value any) error {
	sess, err := s.acp.live(ctx, run)
	if err != nil {
		return err
	}
	isModel := optionID == sess.ModelOptionID()
	effortID := sess.EffortOptionID()
	if err = sess.SetOption(ctx, optionID, value); err != nil {
		return err
	}
	if effortID != "" && optionID == effortID {
		effort := sess.Effort()
		// Claude Code's adapter offers "default", which its CLI flag refuses.
		if effort == "default" {
			effort = ""
		}
		var stored *domain.Run
		if stored, err = s.cfg.Store.GetRun(ctx, run); err != nil {
			return fmt.Errorf("scheduler: load session to record its effort: %w", err)
		}
		if effort == stored.Effort {
			return nil
		}
		if err = domain.ValidEffort(effort); err != nil {
			if nerr := sess.Notice("error", "Effort not recorded", err.Error()); nerr != nil {
				slog.Warn("scheduler: record the effort notice", "run", run, "error", nerr)
			}
			return nil
		}
		if err = s.cfg.Store.SetRunEffort(ctx, run, effort); err != nil {
			return fmt.Errorf("scheduler: record the session's effort: %w", err)
		}
		s.publish(ctx, events.Event{WorkspaceID: stored.WorkspaceID, RunID: run, Payload: events.RunEffortPayload{Effort: effort}})
		return nil
	}
	model := sess.Model()
	if !isModel || model == "" {
		return nil
	}
	stored, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return fmt.Errorf("scheduler: load session to record its model: %w", err)
	}
	if model == stored.Model {
		return nil
	}
	if err = domain.ValidModel(model); err != nil {
		if nerr := sess.Notice("error", "Model not recorded", err.Error()); nerr != nil {
			slog.Warn("scheduler: record the model notice", "run", run, "error", nerr)
		}
		return nil
	}
	if err = s.cfg.Store.SetRunModel(ctx, run, model); err != nil {
		return fmt.Errorf("scheduler: record the session's model: %w", err)
	}
	s.publish(ctx, events.Event{WorkspaceID: stored.WorkspaceID, RunID: run, Payload: events.RunModelPayload{Model: model}})
	return nil
}

var errACPBusy = errors.New("scheduler: the Enhanced agent is busy")

// Wakeable reports whether the server can prompt run's agent session: an
// idle enhanced session, or a Standard Codex terminal attached to its
// app-server.
func (s *Scheduler) Wakeable(run domain.RunID) bool {
	if s.attachedAppServer(run) {
		return s.appServer.connected(run) != nil
	}
	return s.IdleEnhanced(run)
}

func (s *Scheduler) Wake(ctx context.Context, run domain.RunID, prompt string) error {
	if !s.attachedAppServer(run) {
		return s.WakeEnhanced(ctx, run, prompt)
	}
	return s.appServer.wake(ctx, run, prompt)
}

// InTurn reports a Standard Codex terminal in a turn; an enhanced session
// is woken only when idle.
func (s *Scheduler) InTurn(run domain.RunID) bool {
	if !s.attachedAppServer(run) {
		return false
	}
	client := s.appServer.connected(run)
	return client != nil && client.Busy()
}

// attachedAppServer reports a live, unpaused run whose terminal is attached
// to its app-server.
func (s *Scheduler) attachedAppServer(run domain.RunID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	return entry != nil && entry.appServer && !entry.paused
}

func (s *Scheduler) IdleEnhanced(run domain.RunID) bool {
	s.mu.Lock()
	entry := s.runs[run]
	enhanced := entry != nil && entry.launchMode == domain.LaunchACP && !entry.paused && entry.switching == ""
	s.mu.Unlock()
	if !enhanced {
		return false
	}
	sess := s.acp.session(run)
	if sess == nil {
		return false
	}
	st := sess.State()
	return !st.TurnInFlight && st.Queued == 0
}

func (s *Scheduler) WakeEnhanced(ctx context.Context, run domain.RunID, prompt string) error {
	if !s.IdleEnhanced(run) {
		return errACPBusy
	}
	sess, err := s.acp.live(ctx, run)
	if err != nil {
		return err
	}
	blocks := []acp.ContentBlock{acp.TextBlock(prompt)}
	instructions, settle := s.acp.claimInstructions(run, sess)
	if instructions != "" {
		blocks = append(blocks, acp.TextBlock("\n\n"+instructions))
	}
	started, err := sess.PromptIdle(ctx, blocks, settle)
	if err == nil && !started {
		err = errACPBusy
	}
	if !started && ctx.Err() == nil {
		// No turn took the prompt; one awaiting acceptance settles through
		// the callback.
		settle(err)
	}
	return err
}
