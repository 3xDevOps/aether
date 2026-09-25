package coord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
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
		r, err := s.cfg.Store.GetRun(ctx, run)
		if err != nil {
			slog.Warn("coord: overlap notice skipped", "run", run, "peer", peer.RunID, "error", err)
			continue
		}
		if r.Mode != domain.LaunchTUI {
			continue
		}
		err = s.cfg.PTY.Inject(ctx, ptyhost.RunSession(run), noticeActor, "", text, harness.SubmitSequence(r.Harness))
		switch {
		case err == nil:
			s.markNotified(run, peer.RunID)
			s.stampNotice(ctx, r, fmt.Sprintf("coordination notice: run %s is also editing %s",
				peer.RunID, fileList(peer.Files)))
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

// stampNotice records a delivered notice on the notified run's workspace
// timeline as a server-originated coordination event. It runs only after the
// line actually reached the terminal, so the feed says an agent was told
// rather than that one was meant to be, and a publish failure never unsays it.
func (s *Service) stampNotice(ctx context.Context, r *domain.Run, message string) {
	_, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: r.WorkspaceID,
		RunID:       r.ID,
		ActorID:     "",
		Payload:     events.TimelinePayload{Kind: events.TimelineNote, Message: message},
	})
	if err != nil {
		slog.Warn("coord: timeline stamp failed", "run", r.ID, "error", err)
	}
}

// notifyMessage tells the recipient of a newly stored message, question, or
// reply that its inbox has something, so an interactive agent does not have
// to block on an inbox wait to find out. One line covers a whole burst: the
// run is told once and re-armed by its next inbox read (rearmMessageNotice).
// The inbox remains the source of truth; a lost line loses nothing durable.
func (s *Service) notifyMessage(ctx context.Context, msg *store.RunMessage) {
	if s.cfg.PTY == nil {
		return
	}
	// Sends are concurrent RPCs, so the run is claimed before the write and
	// released if the write fails; marking only on success would let two
	// simultaneous sends both reach the terminal.
	if !s.claimMessageNotice(msg.ToRun) {
		return
	}
	r, err := s.cfg.Store.GetRun(ctx, msg.ToRun)
	if err != nil {
		s.rearmMessageNotice(msg.ToRun)
		slog.Warn("coord: message notice skipped", "run", msg.ToRun, "message_id", msg.ID, "error", err)
		return
	}
	// Every run has a terminal, but a headless harness never reads it: the
	// line would sit unread while the timeline claimed delivery.
	if r.Mode != domain.LaunchTUI {
		s.rearmMessageNotice(msg.ToRun)
		return
	}
	err = s.cfg.PTY.Inject(ctx, ptyhost.RunSession(r.ID), noticeActor, "", messageNoticeText(msg.FromRun), harness.SubmitSequence(r.Harness))
	switch {
	case err == nil:
		s.stampNotice(ctx, r, fmt.Sprintf("coordination notice: message from run %s", msg.FromRun))
	case errors.Is(err, ptyhost.ErrNoSession), errors.Is(err, ptyhost.ErrSessionEnded):
		// No live terminal yet, as right after a restart: the message is
		// safe in the inbox, and the next one tries the terminal again.
		s.rearmMessageNotice(msg.ToRun)
	default:
		s.rearmMessageNotice(msg.ToRun)
		slog.Warn("coord: message notice failed", "run", msg.ToRun, "message_id", msg.ID, "error", err)
	}
}

// claimMessageNotice reports whether run is owed a message notice, taking
// the claim when it is.
func (s *Service) claimMessageNotice(run domain.RunID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messageNoticed[run] {
		return false
	}
	s.messageNoticed[run] = true
	return true
}

// rearmMessageNotice lets the next message reach run's terminal again.
func (s *Service) rearmMessageNotice(run domain.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.messageNoticed, run)
}

// messageNoticeText renders the one line a message burst earns. The sender
// ID is a server-issued ULID, so the fixed text stays shell-inert without
// quoting; see noticeText for why that matters.
func messageNoticeText(from domain.RunID) string {
	return fmt.Sprintf("%s: New coordination message from run %s. Run /usr/local/bin/aether-internal inbox to read it, "+
		"then acknowledge the batch with --ack.", noticeActor, from)
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
		who = fmt.Sprintf("%s, member %s, task %s,", r.ID, shellLiteral(m.DisplayName), shellLiteral(r.Task))
	}
	// The banner can land in the login shell a finished harness leaves
	// behind, so the fixed text carries no quote, semicolon, redirection, or
	// subshell character that shell would act on, and every peer-controlled
	// field goes through shellLiteral.
	return fmt.Sprintf(
		"%s: Overlap: run %s is also editing %s. Use /usr/local/bin/aether-internal status --json to inspect the assignment and peers. "+
			"Use /usr/local/bin/aether-internal send --to RUN-ID --body MESSAGE --idempotency-key KEY to coordinate, and "+
			"/usr/local/bin/aether-internal inbox --wait 30 to read messages. Advisory only - keep working. If the other agent "+
			"does not reply, proceed and note the overlap in your commit.",
		noticeActor, who, fileList(peer.Files)), nil
}

// shellLiteral renders a peer-controlled field - a display name chosen at
// invite-join, a task, a path git lets carry any byte but NUL and '/' - as
// one single-quoted shell word. Control characters become spaces first, so
// the field can neither submit the line early nor carry a terminal escape;
// inside single quotes a shell expands nothing, and each ' is closed,
// escaped, and reopened.
func shellLiteral(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fileList renders the shared paths, naming the first few and counting
// the rest so one banner stays one banner.
func fileList(files []string) string {
	if len(files) == 0 {
		return "the same files"
	}
	named := min(len(files), noticeFiles)
	quoted := make([]string, named)
	for i, f := range files[:named] {
		quoted[i] = shellLiteral(f)
	}
	if len(files) <= noticeFiles {
		return strings.Join(quoted, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(quoted, ", "), len(files)-noticeFiles)
}
