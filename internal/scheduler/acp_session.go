package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
)

var ErrACPItemNotFound = errors.New("scheduler: no such session item")

var errNoItemLog = errors.New("scheduler: the run has no session item log")

type ACPStream struct {
	Replay []acphost.Item
	// Reset means the log was replaced past the cursor: Replay starts from
	// the first item and the viewer drops what it held.
	Reset bool
	Epoch int64
	Seq   int64
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
		last := sess.Log().LastSeq()
		reset := afterSeq > last
		if reset {
			afterSeq = 0
		}
		replay, items, cancel, err := sess.Subscribe(afterSeq)
		if err == nil {
			state := sess.State()
			out := ACPStream{Replay: replay, Reset: reset, Items: items, State: &state, Cancel: cancel, Seq: max(afterSeq, 0)}
			out.Epoch, out.Seq = streamMark(sess.Log(), replay, out.Seq)
			out.OldestSeq = oldestSeq(afterSeq, replay)
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
	if afterSeq > log.LastSeq() {
		out.Reset, afterSeq = true, 0
	}
	if out.Replay, err = log.ReadAfter(acphost.ReplayStart(afterSeq, log.LastSeq()), 0); err != nil {
		release()
		return ACPStream{}, err
	}
	out.Epoch, out.Seq = streamMark(log, out.Replay, max(afterSeq, 0))
	out.OldestSeq = oldestSeq(afterSeq, out.Replay)
	return out, nil
}

func oldestSeq(afterSeq int64, replay []acphost.Item) int64 {
	if len(replay) > 0 && (afterSeq <= 0 || replay[0].Seq > afterSeq+1) {
		return replay[0].Seq
	}
	return 0
}

func streamMark(log *acphost.Log, replay []acphost.Item, seq int64) (int64, int64) {
	if n := len(replay); n > 0 {
		return replay[n-1].Epoch, replay[n-1].Seq
	}
	last, err := log.ReadBefore(math.MaxInt64, 1)
	if err != nil || len(last) == 0 {
		return 0, seq
	}
	return last[0].Epoch, seq
}

func (s *Scheduler) ACPHistory(run domain.RunID, beforeSeq int64, limit int) ([]acphost.Item, error) {
	if beforeSeq <= 0 {
		beforeSeq = math.MaxInt64
	}
	var items []acphost.Item
	err := s.withItemLog(run, func(log *acphost.Log) (err error) {
		items, err = log.ReadBefore(beforeSeq, limit)
		return err
	})
	if errors.Is(err, errNoItemLog) {
		return nil, nil
	}
	return items, err
}

func (s *Scheduler) ACPItem(run domain.RunID, seq int64) (acphost.Item, error) {
	var item acphost.Item
	err := s.withItemLog(run, func(log *acphost.Log) error {
		items, err := log.ReadAfter(seq-1, 1)
		if err != nil {
			return err
		}
		if len(items) == 0 || items[0].Seq != seq {
			return fmt.Errorf("%w: %d", ErrACPItemNotFound, seq)
		}
		item = items[0]
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
	return lastItem(log, acphost.KindModeChange).Mode
}

func lastItem(log *acphost.Log, kind acphost.Kind) acphost.Item {
	for before := int64(math.MaxInt64); ; {
		items, err := log.ReadBefore(before, 512)
		if err != nil || len(items) == 0 {
			return acphost.Item{}
		}
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].Kind == kind {
				return items[i]
			}
		}
		before = items[0].Seq
	}
}

func (s *Scheduler) openItemLog(run domain.RunID) (*acphost.Log, error) {
	log, err := acphost.OpenLogReadOnly(s.cfg.PTY.ItemLogPath(run))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return log, err
}

func (s *Scheduler) ACPAnswer(run domain.RunID, requestID, optionID string, values map[string]any) error {
	sess, err := s.acp.live(run)
	if err != nil {
		return err
	}
	return sess.Answer(requestID, optionID, values)
}

func (s *Scheduler) ACPCancel(ctx context.Context, run domain.RunID) error {
	sess, err := s.acp.live(run)
	if err != nil {
		return err
	}
	return sess.Cancel(ctx)
}

func (s *Scheduler) ACPSetOption(ctx context.Context, run domain.RunID, optionID string, value any) error {
	sess, err := s.acp.live(run)
	if err != nil {
		return err
	}
	return sess.SetOption(ctx, optionID, value)
}

var errACPBusy = errors.New("scheduler: the enhanced session is busy")

func (s *Scheduler) IdleEnhanced(run domain.RunID) bool {
	s.mu.Lock()
	entry := s.runs[run]
	enhanced := entry != nil && entry.launchMode == domain.LaunchACP && !entry.paused
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
	sess, err := s.acp.live(run)
	if err != nil {
		return err
	}
	started, err := sess.PromptIdle(ctx, []acp.ContentBlock{acp.TextBlock(prompt)})
	if err != nil {
		return err
	}
	if !started {
		return errACPBusy
	}
	return nil
}
