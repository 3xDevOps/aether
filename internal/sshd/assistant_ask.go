package sshd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/assistant"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// askWait is how long assistant.ask waits for its turn to end before
// answering with the turn's state, inside a connector tool's budget, and
// askPoll how often it reads the turn meanwhile.
var askWait = 25 * time.Second

const askPoll = 200 * time.Millisecond

// askRetryWindow is how long a keyless prompt the same connector sends
// again names the turn the first started, not a new one.
const askRetryWindow = time.Minute

func init() {
	registerGuarded(protocol.MethodAssistantAsk, permissions.Launch, nil, (*Server).assistantAsk)
}

// assistantAsk serves a connector's prompt, poll or card decision. The turn's
// origin, grant and scopes come from the connector's grant, never from the
// params, and a turn or card is visible only to the grant whose turn it is.
func (s *Server) assistantAsk(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodAssistantAsk
	id, _ := ctx.Value(connIdentityKey{}).(connIdentity)
	if id.connector == "" || fromAssistant(ctx) {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: method + " is for ChatGPT and Claude connectors; the Aether app sends prompts with " + protocol.MethodAssistantPrompt}
	}
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	p, perr := decodeParams[protocol.AssistantAskParams](raw)
	if perr != nil {
		return nil, perr
	}
	modes := 0
	for _, set := range []bool{strings.TrimSpace(p.Prompt) != "", p.TurnID != "", p.CardID != ""} {
		if set {
			modes++
		}
	}
	if modes != 1 {
		return nil, invalidParams("pass exactly one of prompt, turn_id or card_id")
	}
	if p.CardID != "" {
		return s.askDecide(ctx, svc, member, id, p)
	}
	if p.Decision != "" || p.Card != "" {
		return nil, invalidParams("decision and card go with card_id")
	}
	deadline := time.Now().Add(askWait)
	var entry assistant.Entry
	var err error
	if p.TurnID != "" {
		if p.IdempotencyKey != "" {
			return nil, invalidParams("idempotency_key goes with prompt")
		}
		entry, err = grantEntry(ctx, svc, member, id.connector, p.TurnID)
	} else {
		// A slow start of the assistant counts against the wait too: past
		// it, Prompt returns the queued entry for the connector to poll.
		sendCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		entry, err = askPrompt(sendCtx, svc, member, id, p)
	}
	if err != nil {
		return nil, assistantError(err)
	}
	for !settled(entry.State) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, assistantError(ctx.Err())
		case <-time.After(askPoll):
		}
		if entry, err = svc.Entry(ctx, member, entry.ID); err != nil {
			return nil, assistantError(err)
		}
	}
	return s.askResult(ctx, svc, member, id, entry)
}

// grantEntry is the member's queue entry id when grant's connector sent
// it, and ErrNoEntry for any other.
func grantEntry(ctx context.Context, svc AssistantService, member domain.MemberID, grant, id string) (assistant.Entry, error) {
	entry, err := svc.Entry(ctx, member, id)
	if err == nil && (entry.Origin.Door != protocol.AssistantDoorConnector || entry.Origin.Grant != grant) {
		err = fmt.Errorf("%w: %s", assistant.ErrNoEntry, id)
	}
	return entry, err
}

func settled(state string) bool {
	return state != protocol.AssistantQueueQueued && state != protocol.AssistantQueueRunning
}

// askResult is entry's turn as its connector is shown it: the final reply,
// redacted and capped, and each card it raised that is still pending, by
// text when the connector may decide it.
func (s *Server) askResult(ctx context.Context, svc AssistantService, member domain.MemberID, id connIdentity, entry assistant.Entry) (protocol.AssistantAskResult, *protocol.Error) {
	out := protocol.AssistantAskResult{TurnID: entry.ID, State: entry.State}
	switch entry.State {
	case protocol.AssistantQueueInterrupted, protocol.AssistantQueueRemoved:
		out.State = protocol.AssistantAskFailed
		if entry.Error == "" {
			entry.Error = "the prompt was " + entry.State
		}
	}
	if entry.Error != "" {
		text, err := svc.ConnectorReply(ctx, member, entry.Error)
		if err != nil {
			return out, assistantError(err)
		}
		out.Error = text
	}
	for _, cardID := range entry.Cards {
		c, err := svc.GrantCard(ctx, member, id.connector, cardID)
		if errors.Is(err, assistant.ErrNoConfirmation) {
			continue
		}
		if err != nil {
			return out, assistantError(err)
		}
		text := c.Text
		if c.AetherOnly || s.cardRefusal(ctx, id, c) != nil {
			text = protocol.AssistantAskWaitingInAether
		}
		out.PendingCards = append(out.PendingCards, protocol.AssistantAskCard{CardID: cardID, Card: text})
	}
	if len(out.PendingCards) > 0 && out.State == protocol.AssistantAskDone {
		out.State = protocol.AssistantAskWaitingCard
	}
	if entry.Reply != "" {
		reply, err := svc.ConnectorReply(ctx, member, entry.Reply)
		if err != nil {
			return out, assistantError(err)
		}
		out.Reply = reply
	}
	return out, nil
}

// askDecide decides a card a turn of id's grant raised, when the connector
// echoes the card's text exactly and, to approve, holds the scope the
// card's method needs.
func (s *Server) askDecide(ctx context.Context, svc AssistantService, member domain.MemberID, id connIdentity, p protocol.AssistantAskParams) (any, *protocol.Error) {
	if p.Prompt != "" || p.TurnID != "" || p.IdempotencyKey != "" {
		return nil, invalidParams("card_id goes with decision and card only")
	}
	if p.Decision != protocol.AssistantAskApprove && p.Decision != protocol.AssistantAskDecline {
		return nil, invalidParams("decision must be approve or decline")
	}
	c, err := svc.GrantCard(ctx, member, id.connector, p.CardID)
	if err != nil {
		return nil, assistantError(err)
	}
	if c.AetherOnly {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
			"card %s is long or shows a saved preference, so it is decided only in Aether; ask the person to open it there", p.CardID)}
	}
	if p.Card != c.Text {
		return nil, invalidParams(fmt.Sprintf("card text does not match card %s; show the person the card and pass its text exactly", p.CardID))
	}
	approve := p.Decision == protocol.AssistantAskApprove
	if approve {
		if perr := s.cardRefusal(ctx, id, c); perr != nil {
			return nil, &protocol.Error{Code: perr.Code, Message: fmt.Sprintf("card %s is decided only in Aether: %s", p.CardID, perr.Message)}
		}
	}
	if err = svc.Confirm(ctx, member, p.CardID, approve); err != nil {
		return nil, assistantError(err)
	}
	return protocol.AssistantAskResult{CardID: p.CardID, Decision: p.Decision}, nil
}

// cardRefusal refuses id the approval of card c unless both id's scopes
// now and the grant's when c's turn ran cover its method.
func (s *Server) cardRefusal(ctx context.Context, id connIdentity, c assistant.ConnectorCard) *protocol.Error {
	if perr := s.connectorCardRefusal(ctx, id, c.Method, c.Params); perr != nil {
		return perr
	}
	if !c.Origin.Covers(c.Method, ConnectorScopes) {
		return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf("%s is outside the scopes %s held when its turn ran", c.Method, ConnectorLabel(id.connectorClient, id.connector))}
	}
	return nil
}

// connectorCardRefusal refuses id the approval of a card of method unless
// it holds the scope the method's own connector tool needs: control for a
// method no write tool calls.
func (s *Server) connectorCardRefusal(ctx context.Context, id connIdentity, method string, params json.RawMessage) *protocol.Error {
	if method == protocol.MethodRunInputRespond {
		p, err := protocol.DecodeParams[protocol.RunInputRespondParams](params)
		if err != nil {
			return invalidParams(method + ": " + err.Error())
		}
		return s.connectorAnswerRefusal(context.WithValue(ctx, connIdentityKey{}, id), domain.RunID(p.RunID), p.RequestID)
	}
	need := connectorMethodScopes[method]
	if !slices.Contains(need, ConnectorScopeWrite) {
		need = scopeControl
	}
	return scopeRefusal(id, method, need)
}

// askLocks serialize keyless prompts that hash alike.
var askLocks [64]sync.Mutex

// askPrompt queues p's prompt from connector id under the caller's key.
// A keyless prompt the same connector sent within askRetryWindow and still
// queued or running is that turn, so a host's retry reaches it, across a
// restart too; once it ended, asking again starts a new turn.
func askPrompt(ctx context.Context, svc AssistantService, member domain.MemberID, id connIdentity, p protocol.AssistantAskParams) (assistant.Entry, error) {
	key := "connector:" + id.connector + ":" + p.IdempotencyKey
	if p.IdempotencyKey == "" {
		// Overlapping retries of one prompt take turns, so the second
		// finds the turn the first queued instead of queuing another.
		h := fnv.New32a()
		_, _ = h.Write([]byte(id.connector + "\x00" + p.Prompt))
		lock := &askLocks[h.Sum32()%uint32(len(askLocks))]
		lock.Lock()
		defer lock.Unlock()
		entry, ok, err := svc.GrantTurn(ctx, member, id.connector, p.Prompt, time.Now().Add(-askRetryWindow))
		if err != nil || ok {
			return entry, err
		}
		key = "connector:" + id.connector + ":auto:" + rand.Text()
	}
	origin := assistant.ConnectorOrigin(id.connectorClient, id.connector, id.connector, id.scopes)
	entry, err := svc.Prompt(ctx, member, p.Prompt, key, nil, origin)
	if err == nil && (entry.Origin.Door != protocol.AssistantDoorConnector || entry.Origin.Grant != id.connector) {
		err = fmt.Errorf("%w: %s", assistant.ErrNoEntry, entry.ID)
	}
	return entry, err
}

// ConnectorGrantScopes returns the scopes member's connector connectorID
// holds now, or why it no longer acts for them: revoked, or its member or
// device no longer admitted.
func (s *Server) ConnectorGrantScopes(ctx context.Context, member domain.MemberID, connectorID string) ([]string, error) {
	cs, perr := s.connectorStore()
	if perr != nil {
		return nil, perr
	}
	c, err := cs.GetConnector(ctx, connectorID)
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && c.Member != member):
		return nil, errors.New("it was revoked")
	case err != nil:
		return nil, fmt.Errorf("read the connector: %w", err)
	}
	if err = s.CheckConnectorGrant(ctx, ConnectorGrant{Member: c.Member, Device: c.Device, Provider: c.Provider, Subject: c.Subject}); err != nil {
		return nil, err
	}
	return c.Scopes, nil
}
