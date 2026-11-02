package acphost

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/domain"
)

// turnAck resolves when the agent accepts a turn's prompt (its first update
// or request) or answers it.
type turnAck struct {
	done      chan struct{}
	err       error
	delivered func(error)
}

func (s *Session) resolveLocked(a *turnAck, err error) {
	select {
	case <-a.done:
		return
	default:
	}
	if err != nil && s.closingLocked() {
		err = ErrClosed
	}
	a.err = err
	close(a.done)
	if a.delivered == nil {
		return
	}
	if err != nil && !errors.Is(err, ErrClosed) {
		err = fmt.Errorf("acphost: %w: %w", domain.ErrPromptRefused, err)
	}
	s.callback(func() { a.delivered(err) })
}

type queuedPrompt struct {
	blocks    []acp.ContentBlock
	delivered func(error)
	tag       string
}

type tagKey struct{}

// WithTag marks the prompt sent with ctx as delivering what tag names, such
// as a room message id. A keeper reports the tags of prompts that reached
// the agent while their server went away.
func WithTag(ctx context.Context, tag string) context.Context {
	return context.WithValue(ctx, tagKey{}, tag)
}

func tagOf(ctx context.Context) string {
	tag, _ := ctx.Value(tagKey{}).(string)
	return tag
}

// Prompt sends input to the agent. With no turn running it starts one and
// returns once the agent accepted it; a refusal is an error. While a turn
// runs, steer asks the agent to add the input to that turn if it advertises
// steering; otherwise the input waits for the turn to end. delivered, if set,
// is called once for a prompt that started, awaited or joined a turn, in the
// order the agent took its prompts: with nil when the agent accepted it, or
// with why it never did. queued says the prompt waited for a turn to end.
func (s *Session) Prompt(ctx context.Context, blocks []acp.ContentBlock, steer bool, delivered func(queued bool, err error)) (Receipt, error) {
	if len(blocks) == 0 {
		return Receipt{}, errors.New("acphost: empty prompt")
	}
	if err := s.validatePromptImages(blocks); err != nil {
		return Receipt{}, err
	}
	s.mu.Lock()
	if s.closingLocked() {
		s.mu.Unlock()
		return Receipt{}, ErrClosed
	}
	tag := tagOf(ctx)
	if !s.turnActive && !s.autonomous {
		ack := s.startTurnLocked(blocks, report(delivered, false), tag)
		s.mu.Unlock()
		return awaitAccept(ctx, ack)
	}
	if !steer || !s.conn.Info().Steering {
		s.queue = append(s.queue, queuedPrompt{blocks, report(delivered, true), tag})
		s.mu.Unlock()
		return Receipt{Outcome: OutcomeQueued}, nil
	}
	starting := s.starting
	s.mu.Unlock()

	outcome, err := s.conn.steer(ctx, blocks, tag)
	if err != nil {
		return Receipt{}, err
	}
	var cancelErr error
	if outcome == "startedNewTurn" {
		// The agent ignored idleBehavior promptRequired and ran the input as
		// a turn of its own. ACP reports a turn's end only in the
		// session/prompt response, so the host cannot see that turn end,
		// and its next session/prompt would reach a busy agent. Stop it.
		cancelErr = s.conn.cancel(ctx)
	}
	s.mu.Lock()
	switch outcome {
	case OutcomeInjected:
		// The agent adds input only to a turn whose prompt it accepted. A
		// later turn may have started while the steer was answered.
		if starting != nil && s.starting == starting {
			s.acceptedLocked()
		}
		s.userMessageLocked(blocks)
		if delivered != nil {
			s.callback(func() { delivered(false, nil) })
		}
		s.mu.Unlock()
		return Receipt{Outcome: OutcomeInjected}, nil
	case "startedNewTurn":
		// The agent has the input; re-sending it would deliver it twice.
		s.userMessageLocked(blocks)
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{
			Severity:    "warning",
			Title:       "Steered message stopped",
			Description: "The agent started a turn of its own for this message instead of adding it to the running turn, so Aether cancelled that turn. The agent has the message; send a new prompt to continue.",
		}})
		s.mu.Unlock()
		return Receipt{}, errors.Join(fmt.Errorf("acphost: %s: the agent answered startedNewTurn despite idleBehavior promptRequired; its turn was cancelled", methodSteering), cancelErr)
	case "promptRequired":
		// The turn ended before the steer reached the agent.
	default:
		s.mu.Unlock()
		return Receipt{}, fmt.Errorf("acphost: %s: unknown outcome %q", methodSteering, outcome)
	}
	if s.closingLocked() {
		s.mu.Unlock()
		return Receipt{}, ErrClosed
	}
	if !s.turnActive && !s.autonomous {
		ack := s.startTurnLocked(blocks, report(delivered, false), tag)
		s.mu.Unlock()
		return awaitAccept(ctx, ack)
	}
	s.queue = append(s.queue, queuedPrompt{blocks, report(delivered, true), tag})
	s.mu.Unlock()
	return Receipt{Outcome: OutcomeQueued}, nil
}

func report(delivered func(queued bool, err error), queued bool) func(error) {
	if delivered == nil {
		return nil
	}
	return func(err error) { delivered(queued, err) }
}

// awaitAccept reports a turn's prompt sent once the agent accepted it, or
// once promptAcceptGrace passed without a refusal.
func awaitAccept(ctx context.Context, ack *turnAck) (Receipt, error) {
	grace := time.NewTimer(promptAcceptGrace)
	defer grace.Stop()
	select {
	case <-ack.done:
		if errors.Is(ack.err, ErrClosed) {
			return Receipt{}, ack.err
		}
		if ack.err != nil {
			return Receipt{}, fmt.Errorf("acphost: %w: %w", domain.ErrPromptRefused, ack.err)
		}
	case <-grace.C:
	case <-ctx.Done():
		return Receipt{}, ctx.Err()
	}
	return Receipt{Outcome: OutcomeSent}, nil
}

// PromptIdle starts a turn only when no prompt or autonomous turn is running
// or owed and no prompt is queued, and reports whether it did once the agent
// accepted the prompt. delivered, if set, is called once for a prompt that
// started a turn, as for Prompt.
func (s *Session) PromptIdle(ctx context.Context, blocks []acp.ContentBlock, delivered func(error)) (bool, error) {
	if err := s.validatePromptImages(blocks); err != nil {
		return false, err
	}
	s.mu.Lock()
	if s.closingLocked() {
		s.mu.Unlock()
		return false, ErrPromptUnsent
	}
	if s.turnActive || s.autonomous || s.owingLocked() || len(s.queue) > 0 {
		s.mu.Unlock()
		return false, nil
	}
	ack := s.startTurnLocked(blocks, delivered, tagOf(ctx))
	s.mu.Unlock()
	if _, err := awaitAccept(ctx, ack); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Session) validatePromptImages(blocks []acp.ContentBlock) error {
	if !s.conn.Info().PromptImages {
		for _, block := range blocks {
			if block.Image != nil {
				return ErrUnsupportedImage
			}
		}
	}
	return nil
}

func (s *Session) startTurnLocked(blocks []acp.ContentBlock, delivered func(error), tag string) *turnAck {
	// Before the turn is recorded: Cancel does not take mu, and a cancel
	// sent after the turn shows in the log must wait for its prompt.
	sent := s.conn.w.sendingPrompt(tag, s.turn+1)
	ack := &turnAck{done: make(chan struct{}), delivered: delivered}
	s.starting = ack
	s.turn++
	s.turnActive, s.holdIdle, s.owedUntil = true, false, time.Time{}
	s.turnAt = time.Now()
	s.emitLocked(Item{Kind: KindTurnStart})
	s.relogTasksLocked()
	s.userMessageLocked(blocks)
	s.stateLocked(TurnState{Working: true, Reason: "prompt"})
	if s.cfg.TitleLookup > 0 && s.proj.title == "" && s.conn.Info().List {
		time.AfterFunc(s.cfg.TitleLookup, s.lookupTitle)
	}
	go s.runTurn(blocks, ack, sent)
	return ack
}

// lookupTitle adopts the title session/list reports for this session. An
// agent lists a session it has not titled under its latest prompt, which is
// not a title.
func (s *Session) lookupTitle() {
	s.mu.Lock()
	prompted := s.prompted
	skip := s.closed || s.proj.title != "" || prompted == ""
	s.mu.Unlock()
	if skip {
		return
	}
	var cursor string
	for range titleLookupPages {
		sessions, next, err := s.conn.listSessions(s.ctx, s.cfg.Cwd, cursor)
		if err != nil {
			if s.ctx.Err() == nil {
				s.logger.Warn("acphost: look up the session title", "err", err)
			}
			return
		}
		i := slices.IndexFunc(sessions, func(listed SessionSummary) bool { return listed.SessionID == s.conn.SessionID() })
		if i >= 0 {
			title := strings.Join(strings.Fields(sessions[i].Title), " ")
			s.mu.Lock()
			if s.proj.title == "" && !strings.HasPrefix(prompted, strings.TrimSpace(strings.TrimSuffix(title, "…"))) {
				s.proj.title = title
				s.emitLocked(Item{Kind: KindSessionInfo, Title: title})
			}
			s.mu.Unlock()
			return
		}
		if next == "" {
			return
		}
		cursor = next
	}
}

// acceptedLocked resolves the starting turn's prompt as accepted.
func (s *Session) acceptedLocked() {
	if s.starting != nil {
		s.resolveLocked(s.starting, nil)
		s.starting = nil
	}
}

func (s *Session) userMessageLocked(blocks []acp.ContentBlock) {
	s.proj.closeText()
	var text strings.Builder
	var attachments []Content
	var forAgent []acp.ContentBlock
	for _, b := range blocks {
		if forAgentOnly(b) {
			forAgent = append(forAgent, b)
			continue
		}
		if b.Text != nil {
			text.WriteString(b.Text.Text)
		} else {
			attachments = append(attachments, contentRef(b))
		}
	}
	s.prompted = strings.Join(strings.Fields(text.String()), " ")
	s.message = UserMessage{Turn: s.turn, MessageID: rand.Text(), Text: text.String(), Context: forAgent}
	body, cut := cutTail(text.String(), maxTextSegment)
	s.emitLocked(Item{Kind: KindMessage, Truncated: cut, Message: &Message{
		Role: "user", MessageID: s.message.MessageID, Text: body, Attachments: attachments, Complete: true,
	}})
}

// UserMessage is the latest prompt's text as the member wrote it, without
// the blocks meant for the agent alone, which Context holds, and the turn
// it was sent in.
type UserMessage struct {
	Turn      int64
	MessageID string
	Text      string
	Context   []acp.ContentBlock
}

// CurrentPrompt is the latest prompt while the turn it started or joined
// still runs; false during an autonomous turn, once the turn ended and on a
// restored session before its first prompt.
func (s *Session) CurrentPrompt() (UserMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.turnActive || s.closingLocked() || s.message.MessageID == "" || s.message.Turn != s.turn {
		return UserMessage{}, false
	}
	return s.message, true
}

// forAgentOnly reports a block annotated for the agent's audience alone,
// which the transcript does not record.
func forAgentOnly(b acp.ContentBlock) bool {
	var a *acp.Annotations
	switch {
	case b.Text != nil:
		a = b.Text.Annotations
	case b.Resource != nil:
		a = b.Resource.Annotations
	}
	return a != nil && len(a.Audience) > 0 && !slices.Contains(a.Audience, acp.RoleUser)
}

// runTurn runs the turn blocks start, or with no blocks the adopted turn
// whose prompt an earlier server process sent.
func (s *Session) runTurn(blocks []acp.ContentBlock, ack *turnAck, sent func()) {
	stop, spent, err := s.conn.prompt(s.ctx, blocks)
	sent()
	lost := blocks == nil && errors.Is(err, errNothingToAdopt)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended++
	s.turnEnded.Broadcast()
	if err != nil && s.conn.closed() {
		// Record what the agent sent before it went away ahead of the
		// interruption. Its answer barrier, released above, is among it.
		s.mu.Unlock()
		s.conn.delivered()
		s.mu.Lock()
	}
	if err != nil && s.detached && s.conn.closed() {
		s.silent = true
	}
	s.resolveLocked(ack, err)
	if s.starting == ack {
		s.starting = nil
	}
	if s.closed {
		return
	}
	s.proj.endTurn()
	s.compacting = false
	reason := stop
	var failed error
	switch {
	case lost:
		reason = "interrupted"
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: "Aether's server restarted and the agent's keeper no longer had the turn's prompt in flight."}})
	case err != nil && (s.conn.closed() || s.conn.inputBroken()):
		reason = "interrupted"
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: err.Error()}})
	case s.hostClosed && (err != nil || stop == "cancelled"):
		// The agent answered its input closing; a restore must resume the
		// turn, not take it as cancelled or failed.
		reason = "interrupted"
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: "Aether closed the agent connection before the turn finished."}})
	case err != nil:
		reason, failed = "error", err
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Prompt failed", Description: err.Error()}})
	}
	if reason == "cancelled" {
		s.endedInTurn = false
		s.dismissLocked()
	}
	s.spend.InputTokens += spent.InputTokens
	s.spend.OutputTokens += spent.OutputTokens
	if err == nil && s.cfg.ClaudeCode && len(blocks) > 0 && blocks[0].Text != nil {
		if words := strings.Fields(blocks[0].Text.Text); len(words) > 0 && slices.Contains(claudeCostResets, words[0]) {
			s.reportSpendLocked()
			s.spend, s.spendDue = Spend{FromZero: true}, true
		}
	}
	s.emitLocked(Item{Kind: KindTurnEnd, StopReason: reason})
	s.turnActive = false
	if len(s.queue) > 0 && !s.closingLocked() {
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.startTurnLocked(next.blocks, next.delivered, next.tag)
		return
	}
	s.followUpLocked()
	s.idleStop = reason
	switch {
	case failed != nil:
		// Tasks still report back; the failure shows until the agent works
		// again.
		s.holdIdle, s.idleStop = true, ""
		s.stateLocked(TurnState{Reason: reason, Failed: failed})
	case reason == "interrupted":
		s.stateLocked(TurnState{Reason: reason})
	default:
		s.settleLocked()
	}
}

// promptAnswered returns once the turn whose session/prompt the agent just
// answered has ended, so the updates the agent sends after its answer are
// not taken for that turn.
func (s *Session) promptAnswered() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers++
	s.awaitTurnEndsLocked(s.answers)
}

// awaitTurnEndsLocked waits until the ends of the first n turns are recorded.
func (s *Session) awaitTurnEndsLocked(n int64) {
	for s.ended < n && !s.closed {
		s.turnEnded.Wait()
	}
}
