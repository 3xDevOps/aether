package assistant

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// maxQueued is how many prompts may wait behind the running turn.
	maxQueued = 5
	// maxReply bounds the reply an entry keeps.
	maxReply = 64 << 10
	// replyScan bounds how far back the log is read for a turn's reply.
	replyScan = 5000

	restartedWhileTurnRan = "the server restarted while this turn ran; it was not sent again"
)

var (
	// ErrQueueFull means maxQueued prompts already wait.
	ErrQueueFull = fmt.Errorf("assistant: %d prompts are already queued; wait or remove one", maxQueued)
	// ErrNoEntry means the member has no queued prompt with the id.
	ErrNoEntry = errors.New("assistant: no such queued prompt")

	errAlreadySent = errors.New("this prompt was already sent")
	errQueueReset  = errors.New("New conversation removed it")
)

// Entry is one prompt of a member's queue: the entry the member sees, the
// origin with its grant, its turn's final reply and the cards it raised.
type Entry struct {
	protocol.AssistantQueueEntry
	Origin Origin
	Reply  string
	Cards  []string
}

func entryOf(row store.AssistantQueueEntry) (Entry, error) {
	e := Entry{AssistantQueueEntry: protocol.AssistantQueueEntry{ID: row.ID, State: row.State, Text: row.Text, Error: row.Error,
		MessageID: row.MessageID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, Reply: row.Reply, Cards: row.Cards}
	if err := json.Unmarshal([]byte(row.Origin), &e.Origin); err != nil {
		return Entry{}, fmt.Errorf("assistant: decode the origin of queued prompt %s: %w", row.ID, err)
	}
	e.AssistantQueueEntry.Origin = e.Origin.AssistantOrigin
	return e, nil
}

// Changes receives a value after a change to the member's status, queue
// entries or cards; stop ends it.
func (s *Service) Changes(member domain.MemberID) (changes <-chan struct{}, stop func()) {
	return s.member(member).updates()
}

// Entry returns one of the member's queue entries, in any state.
func (s *Service) Entry(ctx context.Context, member domain.MemberID, id string) (Entry, error) {
	row, err := s.cfg.Store.AssistantQueueEntry(ctx, member, id)
	if errors.Is(err, store.ErrNotFound) {
		return Entry{}, fmt.Errorf("%w: %s", ErrNoEntry, id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("assistant: read queued prompt %s: %w", id, err)
	}
	return entryOf(*row)
}

// Prompt queues text from origin under key and returns its entry. A key the
// conversation already holds returns that entry and queues nothing. When
// nothing runs or waits before it, the prompt is sent at once and its
// failure returned; a caller that leaves meanwhile does not stop it, and
// gets the entry as queued.
func (s *Service) Prompt(ctx context.Context, id domain.MemberID, text, key string, view *protocol.AssistantViewContext, origin Origin) (Entry, error) {
	m := s.member(id)
	a, first := m.begin(key)
	if !first {
		select {
		case <-a.done:
		case <-ctx.Done():
			return Entry{}, ctx.Err()
		}
		if a.err != nil || a.entry.ID == "" {
			return a.entry, a.err
		}
		if e, err := s.Entry(ctx, id, a.entry.ID); !errors.Is(err, ErrNoEntry) {
			return e, err
		}
		return a.entry, nil
	}
	entry, err := s.enqueue(ctx, m, text, key, view, origin)
	m.mu.Lock()
	behind := m.running != ""
	m.mu.Unlock()
	// Behind a running entry it waits for that turn, not for this call.
	if err == nil && entry.State == protocol.AssistantQueueQueued && !behind {
		done, want := make(chan error, 1), entry.ID
		if !s.spawn(func() { done <- s.advance(s.ctx, m, want) }) {
			err = ErrClosed
		} else {
			select {
			case err = <-done:
			case <-ctx.Done():
			}
		}
		if got, gerr := s.Entry(context.WithoutCancel(ctx), id, entry.ID); gerr == nil {
			entry = got
		} else {
			err = errors.Join(err, gerr)
		}
	}
	m.finish(key, a, entry, err)
	return entry, err
}

func (s *Service) enqueue(ctx context.Context, m *member, text, key string, view *protocol.AssistantViewContext, origin Origin) (Entry, error) {
	row, err := s.stored(ctx, m.id)
	if err != nil {
		return Entry{}, err
	}
	o, err := json.Marshal(origin)
	if err != nil {
		return Entry{}, fmt.Errorf("assistant: encode the prompt's origin: %w", err)
	}
	var v []byte
	if view != nil {
		if v, err = json.Marshal(view); err != nil {
			return Entry{}, fmt.Errorf("assistant: encode the prompt's view context: %w", err)
		}
	}
	held, created, err := s.cfg.Store.EnqueueAssistantPrompt(ctx, &store.AssistantQueueEntry{ID: "turn-" + strings.ToLower(rand.Text()), MemberID: m.id,
		Generation: row.Generation, Key: key, Text: text, View: string(v), Origin: string(o)}, maxQueued)
	if errors.Is(err, store.ErrAssistantQueueFull) {
		return Entry{}, ErrQueueFull
	}
	if err != nil {
		return Entry{}, fmt.Errorf("assistant: %w", err)
	}
	if created {
		m.notify()
	}
	return entryOf(held)
}

// advance sends the oldest queued prompt of the conversation when no turn
// of the queue runs, moving past those that fail to send and removing those
// of an earlier conversation. It returns the failure of entry want, when
// that is the one that failed.
func (s *Service) advance(ctx context.Context, m *member, want string) error {
	m.queue.Lock()
	defer m.queue.Unlock()
	var wantErr error
	for {
		m.mu.Lock()
		busy := m.running != ""
		m.mu.Unlock()
		if busy {
			return wantErr
		}
		conv, err := s.stored(ctx, m.id)
		if err != nil {
			return errors.Join(wantErr, err)
		}
		rows, err := s.cfg.Store.AssistantQueue(ctx, m.id, protocol.AssistantQueueQueued)
		if err != nil {
			return errors.Join(wantErr, fmt.Errorf("assistant: read the queue: %w", err))
		}
		if len(rows) == 0 {
			return wantErr
		}
		next := rows[0]
		if next.Generation != conv.Generation {
			if _, err = s.cfg.Store.MoveAssistantQueueEntry(ctx, m.id, next.ID, []string{protocol.AssistantQueueQueued},
				store.AssistantQueueMove{State: protocol.AssistantQueueRemoved, Error: errQueueReset.Error()}); err != nil {
				return errors.Join(wantErr, fmt.Errorf("assistant: remove queued prompt %s: %w", next.ID, err))
			}
			m.notify()
			continue
		}
		ok, err := s.cfg.Store.MoveAssistantQueueEntry(ctx, m.id, next.ID, []string{protocol.AssistantQueueQueued},
			store.AssistantQueueMove{State: protocol.AssistantQueueRunning})
		if err != nil {
			return errors.Join(wantErr, fmt.Errorf("assistant: start queued prompt %s: %w", next.ID, err))
		}
		if !ok {
			continue
		}
		m.mu.Lock()
		m.running, m.sentOn = next.ID, nil
		m.mu.Unlock()
		m.notify()
		dispatched, err := s.dispatch(ctx, m, next)
		switch {
		case err == nil:
			return wantErr
		case dispatched:
			// Sent, but its key could not be saved: the caller hears why.
			if next.ID == want {
				wantErr = errors.Join(wantErr, err)
			}
			return wantErr
		case errors.Is(err, ErrTurnRunning), s.ctx.Err() != nil:
			// The agent works on its own, and that turn's end sends it, or
			// the service closes and a restart does.
			s.endEntry(m, next.ID, protocol.AssistantQueueQueued, store.AssistantQueueMove{})
			return wantErr
		case errors.Is(err, errAlreadySent):
			s.endEntry(m, next.ID, protocol.AssistantQueueInterrupted, store.AssistantQueueMove{Error: err.Error(), Sent: true})
			continue
		case errors.Is(err, errQueueReset):
			s.endEntry(m, next.ID, protocol.AssistantQueueRemoved, store.AssistantQueueMove{Error: err.Error()})
			continue
		}
		s.endEntry(m, next.ID, protocol.AssistantQueueFailed, store.AssistantQueueMove{Error: err.Error()})
		if next.ID == want {
			wantErr = err
		}
	}
}

// dispatch sends entry e's prompt, as the member's own prompt always was,
// and reports whether the agent may have it.
func (s *Service) dispatch(ctx context.Context, m *member, e store.AssistantQueueEntry) (bool, error) {
	var view *protocol.AssistantViewContext
	if e.View != "" {
		if err := json.Unmarshal([]byte(e.View), &view); err != nil {
			return false, fmt.Errorf("assistant: decode the prompt's view context: %w", err)
		}
	}
	var origin Origin
	if err := json.Unmarshal([]byte(e.Origin), &origin); err != nil {
		return false, fmt.Errorf("assistant: decode the prompt's origin: %w", err)
	}
	conv := m.conversation()
	if err := m.waitForNotes(ctx, conv); err != nil {
		return false, err
	}
	m.share.RLock()
	defer m.share.RUnlock()
	dispatched, err := s.send(ctx, m, conv, e.Generation, e.Text, e.Key, view, origin)
	if errors.Is(err, errAlreadySent) {
		return false, err
	}
	return dispatched, err
}

// sending records that the running entry's prompt goes to live next.
func (m *member) sending(live *session) {
	turn := live.acp.Turn()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentOn, m.sentAfter = live, turn
}

// endEntry moves m's running entry id to state, and stops counting it as
// running; later calls outside a queued turn raise cards from the
// dashboard.
func (s *Service) endEntry(m *member, id, state string, move store.AssistantQueueMove) {
	move.State = state
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Written before the entry stops counting as running. A write that keeps
	// failing leaves the row running, which the next start marks
	// interrupted; the queue moves on meanwhile.
	var err error
	for attempt := range 3 {
		if _, err = s.cfg.Store.MoveAssistantQueueEntry(ctx, m.id, id, []string{protocol.AssistantQueueRunning}, move); err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}
	if err != nil {
		slog.Warn("assistant: record the queued prompt's end", "member", m.id, "entry", id, "state", state, "error", err)
	}
	m.mu.Lock()
	if m.running == id {
		m.running, m.sentOn, m.origin = "", nil, Origin{}
	}
	m.mu.Unlock()
	m.notify()
}

// onState is the turn state callback of m's session, which sess names once
// known: an idle agent ends the calls waiting for cards and the running
// entry's turn, when that entry went to this session.
func (s *Service) onState(m *member, sess func() *acphost.Session) func(acphost.TurnState) {
	return func(st acphost.TurnState) {
		if !st.Working && st.Tasks == 0 {
			m.confirms.release(st.Turn)
			s.spawn(func() { s.turnIdle(m, sess(), st) })
		}
		m.notify()
	}
}

// turnIdle ends the running entry once the agent is idle after the turn
// its prompt started, with the turn's reply, and sends the next.
func (s *Service) turnIdle(m *member, sess *acphost.Session, st acphost.TurnState) {
	m.mu.Lock()
	id, sent, after := m.running, m.sentOn != nil && sess != nil && m.sentOn.acp == sess, m.sentAfter
	m.mu.Unlock()
	if id != "" && sent && st.Turn > after {
		state, move := protocol.AssistantQueueDone, store.AssistantQueueMove{Sent: true}
		switch {
		case errors.Is(st.Failed, domain.ErrPromptRefused):
			// The agent never took the prompt, so its key is free to retry.
			state, move.Error, move.Sent = protocol.AssistantQueueFailed, st.Failed.Error(), false
		case st.Failed != nil:
			state, move.Error = protocol.AssistantQueueFailed, st.Failed.Error()
		case st.Reason == "cancelled":
			state, move.Error = protocol.AssistantQueueFailed, "you stopped this turn"
		case st.Reason == "interrupted":
			state, move.Error = protocol.AssistantQueueFailed, "the turn was interrupted before it finished"
		}
		var err error
		if move.MessageID, move.Reply, err = s.turnReply(m.id); err != nil {
			why := "read the turn's reply: " + err.Error()
			if move.Error != "" {
				why = move.Error + "; " + why
			}
			state, move.Error = protocol.AssistantQueueFailed, why
		}
		s.endEntry(m, id, state, move)
	}
	if err := s.advance(s.ctx, m, ""); err != nil && s.ctx.Err() == nil {
		slog.Warn("assistant: send the next queued prompt", "member", m.id, "error", err)
	}
}

// sessionEnded fails the running entry sent to session live, which ended
// under it, unless the service is closing, and sends the next.
func (s *Service) sessionEnded(m *member, live *session) {
	if s.ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	id := m.running
	if m.sentOn != live {
		id = ""
	}
	m.mu.Unlock()
	if id != "" {
		s.endEntry(m, id, protocol.AssistantQueueFailed, store.AssistantQueueMove{Error: "the assistant's session ended before the turn finished", Sent: true})
	}
	if err := s.advance(s.ctx, m, ""); err != nil && s.ctx.Err() == nil {
		slog.Warn("assistant: send the next queued prompt", "member", m.id, "error", err)
	}
}

// turnReply reads the newest turn a prompt started from the member's log:
// the id of the prompt's message and the agent's last message in the turn.
func (s *Service) turnReply(id domain.MemberID) (messageID, reply string, err error) {
	var items []acphost.Item
	for before := int64(math.MaxInt64); len(items) < replyScan; {
		page, err := s.History(id, before, protocol.ACPHistoryMaxLimit)
		if err != nil {
			return "", "", fmt.Errorf("read the transcript: %w", err)
		}
		if len(page.Items) == 0 {
			break
		}
		items = append(page.Items, items...)
		for i := len(page.Items) - 1; i >= 0; i-- {
			if it := page.Items[i]; it.Kind == acphost.KindTurnStart && !it.Autonomous {
				return turnText(items, it.Turn)
			}
		}
		before = page.Items[0].Seq
	}
	// The turn's start is further back than the scan reads: its end names
	// the turn, and its last message is in what was read.
	for i := len(items) - 1; i >= 0; i-- {
		if it := items[i]; it.Kind == acphost.KindTurnEnd && !it.Autonomous {
			return turnText(items, it.Turn)
		}
	}
	return "", "", nil
}

func turnText(items []acphost.Item, turn int64) (messageID, reply string, err error) {
	var last string
	texts := map[string]*strings.Builder{}
	for _, it := range items {
		if it.Turn != turn || it.Kind != acphost.KindMessage || it.Message == nil || it.Message.ParentID != "" {
			continue
		}
		switch it.Message.Role {
		case "user":
			if messageID == "" {
				messageID = it.Message.MessageID
			}
		case "assistant":
			last = it.Message.MessageID
			if texts[last] == nil {
				texts[last] = &strings.Builder{}
			}
			texts[last].WriteString(it.Message.Text)
		}
	}
	if last != "" {
		reply = texts[last].String()
	}
	if len(reply) > maxReply {
		reply = strings.ToValidUTF8(reply[:maxReply], "") + "…"
	}
	return messageID, reply, nil
}

// queueOf lists the entries of the member's current conversation that the
// status shows: those waiting and running, then those the store keeps that
// ended.
func (s *Service) queueOf(ctx context.Context, id domain.MemberID, generation int64) ([]protocol.AssistantQueueEntry, error) {
	rows, err := s.cfg.Store.AssistantQueue(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("assistant: read the queue: %w", err)
	}
	var open, ended []protocol.AssistantQueueEntry
	for _, row := range rows {
		if row.Generation != generation || row.State == protocol.AssistantQueueRemoved {
			continue
		}
		e, err := entryOf(row)
		if err != nil {
			return nil, err
		}
		if row.State == protocol.AssistantQueueQueued || row.State == protocol.AssistantQueueRunning {
			open = append(open, e.AssistantQueueEntry)
			continue
		}
		// A prompt in the transcript needs only its door here, not its text.
		if e.MessageID != "" {
			e.Text = ""
		}
		ended = append(ended, e.AssistantQueueEntry)
	}
	return append(open, ended[max(0, len(ended)-store.AssistantQueueKept):]...), nil
}

// RemoveQueued takes a queued prompt off the member's queue before it runs,
// or dismisses one that failed or was interrupted. A failure that never
// reached the agent is deleted, since its key may already name a retry.
func (s *Service) RemoveQueued(ctx context.Context, member domain.MemberID, id string) error {
	ok, err := s.cfg.Store.DeleteUnsentAssistantQueueEntry(ctx, member, id)
	if err == nil && !ok {
		ok, err = s.cfg.Store.MoveAssistantQueueEntry(ctx, member, id,
			[]string{protocol.AssistantQueueQueued, protocol.AssistantQueueFailed, protocol.AssistantQueueInterrupted},
			store.AssistantQueueMove{State: protocol.AssistantQueueRemoved})
	}
	if err != nil {
		return fmt.Errorf("assistant: remove queued prompt %s: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoEntry, id)
	}
	s.member(member).notify()
	return nil
}

// dropQueue deletes the member's queue with the conversation it belongs
// to, prompts and replies included. The caller holds m.op.
func (s *Service) dropQueue(ctx context.Context, m *member) error {
	m.mu.Lock()
	m.running, m.sentOn = "", nil
	m.mu.Unlock()
	if err := s.cfg.Store.DeleteAssistantQueue(ctx, m.id); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	return nil
}

// resumeQueue marks the turns a restart lost interrupted, never sending
// them again, and sends the waiting prompts once runs are recovered.
func (s *Service) resumeQueue(ctx context.Context) error {
	rows, err := s.cfg.Store.AssistantQueue(ctx, "", protocol.AssistantQueueQueued, protocol.AssistantQueueRunning)
	if err != nil {
		return fmt.Errorf("assistant: read the queues: %w", err)
	}
	waiting := map[domain.MemberID]bool{}
	for _, row := range rows {
		if row.State == protocol.AssistantQueueQueued {
			waiting[row.MemberID] = true
			continue
		}
		if _, err = s.cfg.Store.MoveAssistantQueueEntry(ctx, row.MemberID, row.ID, []string{protocol.AssistantQueueRunning},
			store.AssistantQueueMove{State: protocol.AssistantQueueInterrupted, Error: restartedWhileTurnRan}); err != nil {
			return fmt.Errorf("assistant: mark queued prompt %s interrupted: %w", row.ID, err)
		}
	}
	for id := range waiting {
		m := s.member(id)
		ready := s.cfg.Runs.RecoveryReady()
		s.spawn(func() {
			if ready != nil {
				select {
				case <-ready:
				case <-s.ctx.Done():
					return
				}
			}
			if err := s.advance(s.ctx, m, ""); err != nil && s.ctx.Err() == nil {
				slog.Warn("assistant: send the queued prompts after a restart", "member", id, "error", err)
			}
		})
	}
	return nil
}
