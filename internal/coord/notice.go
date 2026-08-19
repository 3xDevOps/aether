package coord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// noticeActor is the attribution the overlap banner carries. It is the
// server speaking, not a member, so it takes no palette color.
const noticeActor = "aether"

// noticeFiles caps how many shared paths the banner names before it
// summarizes the rest; the agent can see the whole picture with
// coord.status.
const noticeFiles = 3

// notify injects the advisory overlap banner into run's terminal, once per
// peer. The notice re-arms when an overlap clears, so a pair that conflicts
// again later is told again - but a persisting overlap never repeats it.
//
// A peer counts as announced only once its banner has actually reached the
// terminal. Marking it before the attempt would spend the pair's one
// notice on a failure and never retry it while the overlap lasts, and the
// failure is routine rather than exotic: after a restart this service is
// consuming overlap events before the scheduler has finished re-attaching
// the surviving containers, so an event landing in that window finds no
// live session at all.
func (s *Service) notify(ctx context.Context, run domain.RunID, with []events.OverlapPeer) {
	if s.cfg.PTY == nil {
		return
	}
	for _, peer := range s.pendingNotices(run, with) {
		text, err := s.noticeText(ctx, peer)
		if err != nil {
			slog.Warn("coord: overlap notice skipped", "run", run, "peer", peer.RunID, "error", err)
			continue
		}
		err = s.cfg.PTY.Inject(ctx, run, noticeActor, "", text)
		switch {
		case err == nil:
			s.markNotified(run, peer.RunID)
			s.stampNotice(ctx, run, peer)
		case errors.Is(err, ptyhost.ErrNoSession), errors.Is(err, ptyhost.ErrSessionEnded):
			// A run without a live terminal is exactly the degradation the
			// design expects: the radar chip still stands for the humans.
			// The peer stays unannounced, so the next overlap change tries
			// again - by which time the terminal may exist.
		default:
			slog.Warn("coord: overlap notice failed", "run", run, "peer", peer.RunID, "error", err)
		}
	}
}

// stampNotice records a delivered notice on the notified run's session
// timeline, attributed to that run's owner - the same audit trail
// coordination messages leave. It runs only after the banner actually
// reached the terminal, so the feed says an agent was told rather than
// that one was meant to be, and a publish failure never unsays it.
func (s *Service) stampNotice(ctx context.Context, run domain.RunID, peer events.OverlapPeer) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("coord: overlap notice not stamped", "run", run, "peer", peer.RunID, "error", err)
		return
	}
	_, err = s.cfg.Bus.Publish(ctx, events.Event{
		SessionID: r.SessionID,
		RunID:     r.ID,
		ActorID:   r.MemberID,
		Payload: events.TimelinePayload{
			Kind: events.TimelineNote,
			Message: fmt.Sprintf("coordination notice: run %s is also editing %s",
				peer.RunID, fileList(peer.Files)),
		},
	})
	if err != nil {
		slog.Warn("coord: timeline stamp failed", "run", run, "peer", peer.RunID, "error", err)
	}
}

// pendingNotices forgets the peers run no longer overlaps - which is what
// re-arms the notice for a pair that collides again later - and returns
// the peers it still owes a banner.
func (s *Service) pendingNotices(run domain.RunID, with []events.OverlapPeer) []events.OverlapPeer {
	s.mu.Lock()
	defer s.mu.Unlock()
	sent := s.noticed[run]
	live := make(map[domain.RunID]bool, len(with))
	var pending []events.OverlapPeer
	for _, peer := range with {
		live[peer.RunID] = true
		if !sent[peer.RunID] {
			pending = append(pending, peer)
		}
	}
	for id := range sent {
		if !live[id] {
			delete(sent, id)
		}
	}
	if len(sent) == 0 {
		delete(s.noticed, run)
	}
	return pending
}

// markNotified records that a peer's banner reached run's terminal, so a
// persisting overlap does not repeat it.
func (s *Service) markNotified(run, peer domain.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sent := s.noticed[run]
	if sent == nil {
		sent = make(map[domain.RunID]bool, 1)
		s.noticed[run] = sent
	}
	sent[peer] = true
}

// noticeText renders the banner: who the peer is, what they are doing,
// which files collide, and that none of it is binding.
func (s *Service) noticeText(ctx context.Context, peer events.OverlapPeer) (string, error) {
	r, err := s.cfg.Store.GetRun(ctx, peer.RunID)
	if err != nil {
		return "", fmt.Errorf("coord: resolve peer run %s: %w", peer.RunID, err)
	}
	who := string(r.ID)
	if m, merr := s.cfg.Store.GetMember(ctx, r.MemberID); merr == nil {
		// The display name is chosen at invite-join and unsanitized; %q keeps
		// its control characters out of the terminal and the agent's stdin.
		who = fmt.Sprintf("%s (%q - %q)", r.ID, m.DisplayName, r.Task)
	}
	return fmt.Sprintf(
		"[%s] Overlap: run %s is also editing %s. You have aether MCP tools to coordinate "+
			"(aether_status, aether_send, aether_inbox). Advisory only - keep working; if the "+
			"other agent doesn't reply, proceed and note the overlap in your commit.",
		noticeActor, who, fileList(peer.Files)), nil
}

// fileList renders the shared paths, naming the first few and counting
// the rest so one banner stays one banner. Git allows any byte but NUL
// and '/' in a path, so each is quoted like the display name: %q keeps a
// crafted filename's control characters out of the terminal.
func fileList(files []string) string {
	if len(files) == 0 {
		return "the same files"
	}
	named := min(len(files), noticeFiles)
	quoted := make([]string, named)
	for i, f := range files[:named] {
		quoted[i] = fmt.Sprintf("%q", f)
	}
	if len(files) <= noticeFiles {
		return strings.Join(quoted, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(quoted, ", "), len(files)-noticeFiles)
}
