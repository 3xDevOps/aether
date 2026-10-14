package assistant

import (
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// Stream is a member's assistant session as a viewer sees it: the session
// stream of a run, and a signal to read the status again.
type Stream struct {
	scheduler.ACPStream
	// Updates receives a value after each change of the status.
	Updates <-chan struct{}
}

// Subscribe replays the item log after afterSeq and, while a session is
// live, follows it. Started is closed when the session starts or ends or the
// conversation is reset, and the viewer resubscribes.
func (s *Service) Subscribe(id domain.MemberID, afterSeq int64) (Stream, error) {
	m := s.member(id)
	m.mu.Lock()
	live, changed := m.liveLocked(), m.changed
	m.mu.Unlock()
	updates, stopWatching := m.updates()
	out := Stream{Updates: updates}
	out.Started, out.Cancel = changed, stopWatching
	if live != nil {
		replay, items, cancel, err := live.acp.Subscribe(afterSeq)
		if err == nil {
			state := live.acp.State()
			out.ACPStream = scheduler.ACPStream{
				Replay: replay.Items, Reset: replay.Reset, TruncatedBefore: replay.TruncatedBefore, Epoch: replay.Epoch,
				Seq: replay.Seq, OldestSeq: replay.OldestSeq, Items: items, State: &state, Started: changed,
				Cancel: func() { cancel(); stopWatching() },
			}
			return out, nil
		}
		if !errors.Is(err, acphost.ErrClosed) {
			stopWatching()
			return Stream{}, err
		}
		out.Started = live.ended
	}
	log, err := s.openLog(id)
	if err != nil {
		stopWatching()
		return Stream{}, err
	}
	if log == nil {
		return out, nil
	}
	defer func() { _ = log.Close() }()
	replay, err := log.Replay(afterSeq)
	if err != nil {
		stopWatching()
		return Stream{}, err
	}
	out.Replay, out.Reset, out.TruncatedBefore = replay.Items, replay.Reset, replay.TruncatedBefore
	out.Epoch, out.Seq, out.OldestSeq = replay.Epoch, replay.Seq, replay.OldestSeq
	return out, nil
}

// History pages the item log backwards from beforeSeq.
func (s *Service) History(id domain.MemberID, beforeSeq int64, limit int) (acphost.HistoryPage, error) {
	if beforeSeq <= 0 {
		beforeSeq = math.MaxInt64
	}
	if live := s.member(id).live(); live != nil {
		page, err := live.acp.Log().History(beforeSeq, limit)
		if !errors.Is(err, acphost.ErrLogClosed) {
			return page, err
		}
	}
	log, err := s.openLog(id)
	if err != nil || log == nil {
		return acphost.HistoryPage{}, err
	}
	defer func() { _ = log.Close() }()
	return log.History(beforeSeq, limit)
}

// openLog opens the item log read-only; nil when there is none.
func (s *Service) openLog(id domain.MemberID) (*acphost.Log, error) {
	log, err := acphost.OpenLogReadOnly(s.logPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return log, err
}

// Item reads one item of the item log.
func (s *Service) Item(id domain.MemberID, seq int64) (acphost.Item, error) {
	read := func(log *acphost.Log) (acphost.Item, error) {
		it, found, err := log.Item(seq)
		if err == nil && !found {
			err = fmt.Errorf("%w: %d", scheduler.ErrACPItemNotFound, seq)
		}
		return it, err
	}
	if live := s.member(id).live(); live != nil {
		it, err := read(live.acp.Log())
		if !errors.Is(err, acphost.ErrLogClosed) {
			return it, err
		}
	}
	log, err := s.openLog(id)
	if err != nil {
		return acphost.Item{}, err
	}
	if log == nil {
		return acphost.Item{}, fmt.Errorf("%w: %d", scheduler.ErrACPItemNotFound, seq)
	}
	defer func() { _ = log.Close() }()
	return read(log)
}
