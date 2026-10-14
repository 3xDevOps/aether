package assistant

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

type Stream struct {
	scheduler.ACPStream
	// Generation is the conversation the stream replays; ACPStream.Epoch
	// carries it, so a viewer of an earlier conversation starts over.
	Generation int64
	// Updates receives a value after each change of the status.
	Updates <-chan struct{}
}

// Epoch is the epoch a viewer sees for the log's epoch in a generation; it is
// never zero, which a viewer reports before it has seen any.
func Epoch(generation, logEpoch int64) int64 { return (generation+1)*EpochStride + logEpoch }

// Generation is the member's current conversation. It waits for a reset in
// progress, so a viewer whose stream ended with the session reads the new one.
func (s *Service) Generation(ctx context.Context, id domain.MemberID) (int64, error) {
	m := s.member(id)
	m.reset.RLock()
	defer m.reset.RUnlock()
	row, err := s.stored(ctx, id)
	if err != nil {
		return 0, err
	}
	return row.Generation, nil
}

// Subscribe replays the item log after afterSeq and, while a session is
// live, follows it. Started is closed when the session starts or ends or the
// conversation is reset, and the viewer resubscribes. A viewer that reports
// the epoch of an earlier conversation gets the whole log.
func (s *Service) Subscribe(ctx context.Context, id domain.MemberID, afterSeq, viewerEpoch int64) (Stream, error) {
	generation, genErr := s.Generation(ctx, id)
	if genErr != nil {
		return Stream{}, genErr
	}
	if viewerEpoch != 0 && viewerEpoch/EpochStride-1 != generation {
		afterSeq = 0
	}
	m := s.member(id)
	m.mu.Lock()
	live, changed := m.liveLocked(), m.changed
	m.mu.Unlock()
	updates, stopWatching := m.updates()
	out := Stream{Updates: updates, Generation: generation}
	out.Epoch = Epoch(generation, 0)
	out.Started, out.Cancel = changed, stopWatching
	if live != nil {
		replay, items, cancel, err := live.acp.Subscribe(afterSeq)
		if err == nil {
			state := live.acp.State()
			out.ACPStream = scheduler.ACPStream{
				Replay: replay.Items, Reset: replay.Reset, TruncatedBefore: replay.TruncatedBefore, Epoch: Epoch(generation, replay.Epoch),
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
	out.Epoch, out.Seq, out.OldestSeq = Epoch(generation, replay.Epoch), replay.Seq, replay.OldestSeq
	return out, nil
}

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

func (s *Service) openLog(id domain.MemberID) (*acphost.Log, error) {
	log, err := acphost.OpenLogReadOnly(s.logPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return log, err
}

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
