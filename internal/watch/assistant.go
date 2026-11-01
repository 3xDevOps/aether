package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// AssistantNotice is a notice of a watch the assistant created, for its
// owner's assistant conversation.
type AssistantNotice struct {
	WatchID     string
	WorkspaceID domain.WorkspaceID
	TargetKind  string
	RunID       domain.RunID
	Condition   string
	Key         string
	Title       string
	Body        string
	Link        string
	WatchLink   string
}

// ErrNotAnnounced is a notice written into the conversation whose toast
// could not be raised; it is recorded delivered, with the reason.
var ErrNotAnnounced = errors.New("written to the conversation, but its toast was not raised")

// Assistant writes notices into a member's assistant conversation.
type Assistant interface {
	Transcribe(ctx context.Context, member domain.MemberID, n AssistantNotice) error
}

// UseAssistant has the notices of watches whose source is the assistant
// written into their owner's assistant conversation. Call it before Start.
func (s *Service) UseAssistant(a Assistant) { s.assistant = a }

// Follow watches run for member on the assistant's behalf, on the run's
// default conditions: one assistant watch per member and run, returned as
// it stands, paused or not, when there is one, and renewed if it expired.
// A run relaunched after its watch was done gets a new watch, keyed by the
// one before, since a done watch never announces the same end twice. A
// need the run stands in now is taken as seen: the assistant just launched
// the run or wrote to it, so only the next one is news. A live assistant
// watch on the run's default conditions without the key, such as one
// aether_watch made an assistant watch, is that watch too.
func (s *Service) Follow(ctx context.Context, member domain.MemberID, run domain.RunID) (protocol.WatchResult, error) {
	conditions, err := s.normalize(protocol.WatchTargetRun, nil)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	watches, err := s.store.ListWatches(ctx, store.WatchFilter{MemberID: member, TargetKind: protocol.WatchTargetRun, RunID: run})
	if err != nil {
		return protocol.WatchResult{}, err
	}
	if i := slices.IndexFunc(watches, func(w *store.Watch) bool {
		return w.Source == protocol.WatchSourceAssistant && slices.Equal(w.Conditions, conditions) &&
			w.State != protocol.WatchDone && w.State != protocol.WatchExpired
	}); i >= 0 {
		return s.result(ctx, watches[i])
	}
	key := "assistant:" + string(run)
	for {
		res, err := s.create(ctx, member, protocol.WatchCreateParams{
			Target:         protocol.WatchTarget{Kind: protocol.WatchTargetRun, RunID: string(run)},
			Source:         protocol.WatchSourceAssistant,
			IdempotencyKey: key,
		}, true)
		if err != nil {
			return res, err
		}
		switch res.Watch.State {
		case protocol.WatchExpired:
			return s.renew(ctx, member, res.Watch.ID)
		case protocol.WatchDone:
			r, err := s.store.GetRun(ctx, run)
			if err != nil {
				return protocol.WatchResult{}, fmt.Errorf("session %s: %w", run, err)
			}
			if r.Status.Final() {
				return res, nil
			}
			key = "assistant:" + string(run) + "|" + res.Watch.ID
			continue
		}
		return res, nil
	}
}

// renew restarts member's expired assistant watch id for DefaultExpiry,
// taking the need its run stands in now as seen, as a new watch would.
func (s *Service) renew(ctx context.Context, member domain.MemberID, id string) (protocol.WatchResult, error) {
	for range 4 {
		w, err := s.owned(ctx, member, id)
		if err != nil {
			return protocol.WatchResult{}, err
		}
		if w.State != protocol.WatchExpired {
			return s.result(ctx, w)
		}
		now := s.now()
		w.State, w.EndedAt, w.UpdatedAt, w.ExpiresAt, w.NextCheckAt = protocol.WatchActive, nil, now, now.Add(DefaultExpiry), &now
		if err = s.baseline(ctx, w); err != nil {
			return protocol.WatchResult{}, err
		}
		err = s.store.UpdateWatch(ctx, w)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			return protocol.WatchResult{}, err
		}
		return s.settled(context.WithoutCancel(ctx), w.ID)
	}
	return protocol.WatchResult{}, fmt.Errorf("watch %s: changed by every concurrent update", id)
}

// baseline records the need w's target stands in now as already fired.
func (s *Service) baseline(ctx context.Context, w *store.Watch) error {
	r, err := s.read(ctx, w, protocol.WatchObserved{})
	if err != nil {
		return err
	}
	if w.Fired == nil {
		w.Fired = map[string]string{}
	}
	var keys []string
	for _, n := range r.notices {
		if n.Condition == protocol.WatchNeedsYou {
			keys = append(keys, n.Key)
		}
	}
	if len(keys) > 0 {
		w.Fired[protocol.WatchNeedsYou] = strings.Join(keys, firedSep)
	}
	return nil
}

// ownerPushed reports whether internal/push already pushes n to w's owner
// as the run's own notification. The owner of a run is
// pushed what push.needOf finds: every need of it, a reported success
// while it waits for review and a failure nobody has seen, so only its
// other ends are news to their devices. A success the owner already put
// off or accepted is not pushed by the run. Push, started before the watch
// service, does not announce a standing entered before it started, which
// a watch evaluated on recovery then pushes itself.
func ownerPushed(w *store.Watch, run *domain.Run, n Notice, started time.Time) bool {
	if run == nil || run.MemberID != w.MemberID {
		return false
	}
	if strings.HasPrefix(n.Key, "input:") {
		return true
	}
	if run.StatusChangedAt != nil && run.StatusChangedAt.Before(started) {
		return false
	}
	switch n.Condition {
	case protocol.WatchNeedsYou:
		return true
	case protocol.WatchFinished:
		return strings.HasPrefix(n.Key, "reported|") && run.Review == domain.ReviewReady || n.Key == "failed" && run.OutcomeUnseen
	}
	return false
}

// deliverAssistant writes the pending assistant notices, each member's in
// order on a goroutine of its own, so one member's busy conversation does
// not hold back another's. The list is read under amu, so a notice a
// member's goroutine is writing is either still owned by it or already
// recorded.
func (s *Service) deliverAssistant(ctx context.Context) {
	if s.assistant == nil {
		return
	}
	s.amu.Lock()
	defer s.amu.Unlock()
	byMember, order, err := s.pendingAssistant(ctx)
	if err != nil {
		return
	}
	for _, member := range order {
		if s.transcribing[member] {
			continue
		}
		s.transcribing[member] = true
		notes := byMember[member]
		s.wg.Go(func() {
			for {
				for _, n := range notes {
					if err := s.transcribe(ctx, n); err != nil {
						if ctx.Err() == nil {
							slog.Warn("watch: record an assistant delivery", "notification", n.ID, "error", err)
						}
						s.amu.Lock()
						delete(s.transcribing, member)
						s.amu.Unlock()
						return
					}
				}
				s.amu.Lock()
				byMember, _, err := s.pendingAssistant(ctx)
				if notes = byMember[member]; err != nil || len(notes) == 0 {
					delete(s.transcribing, member)
					s.amu.Unlock()
					return
				}
				s.amu.Unlock()
			}
		})
	}
}

// pendingAssistant is the pending assistant notices by member, and the
// members in the order of their oldest.
func (s *Service) pendingAssistant(ctx context.Context) (map[domain.MemberID][]*store.WatchNotification, []domain.MemberID, error) {
	pending, err := s.store.PendingWatchAssistantNotices(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("watch: list pending assistant notices", "error", err)
		}
		return nil, nil, err
	}
	byMember := map[domain.MemberID][]*store.WatchNotification{}
	var order []domain.MemberID
	for _, n := range pending {
		if byMember[n.MemberID] == nil {
			order = append(order, n.MemberID)
		}
		byMember[n.MemberID] = append(byMember[n.MemberID], n)
	}
	return byMember, order, nil
}

// transcribe writes n into its owner's assistant conversation and records
// the result, even when ctx ended after the write. A notice cut short by
// shutdown stays pending for the next start, and one of a paused watch
// until it is resumed.
func (s *Service) transcribe(ctx context.Context, n *store.WatchNotification) error {
	w, err := s.store.GetWatch(ctx, n.WatchID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if w.State == protocol.WatchPaused {
		return nil
	}
	err = s.assistant.Transcribe(ctx, n.MemberID, AssistantNotice{
		WatchID: w.ID, WorkspaceID: w.WorkspaceID, TargetKind: w.TargetKind, RunID: w.RunID,
		Condition: n.Condition, Key: n.Key, Title: w.TargetTitle, Body: n.Body, Link: n.Link, WatchLink: WatchLink(w.ID),
	})
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	d := store.WatchDelivery{Channel: protocol.WatchChannelAssistant, Status: protocol.WatchStatusDelivered, At: s.now()}
	switch {
	case errors.Is(err, ErrNotAnnounced):
		d.Detail = err.Error()
	case err != nil:
		d.Status, d.Detail = protocol.WatchStatusFailed, err.Error()
	}
	if err := s.store.SetWatchDelivery(ctx, n.ID, d); err != nil {
		return err
	}
	s.changed(ctx, w, "")
	return nil
}
