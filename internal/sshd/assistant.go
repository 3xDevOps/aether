package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/assistant"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

type AssistantService interface {
	Status(ctx context.Context, member domain.MemberID) (protocol.AssistantStatus, error)
	Start(ctx context.Context, member domain.MemberID, p protocol.AssistantStartParams) (protocol.AssistantStatus, error)
	Prompt(ctx context.Context, member domain.MemberID, text, idempotencyKey string) error
	Cancel(ctx context.Context, member domain.MemberID) error
	Answer(member domain.MemberID, requestID, optionID string, values map[string]any) error
	Confirm(member domain.MemberID, confirmationID string, approve bool) error
	Reset(ctx context.Context, member domain.MemberID) (protocol.AssistantStatus, error)
	History(member domain.MemberID, beforeSeq int64, limit int) (acphost.HistoryPage, error)
	Item(member domain.MemberID, seq int64) (acphost.Item, error)
	Remove(ctx context.Context, member domain.MemberID) error
	Subscribe(ctx context.Context, member domain.MemberID, afterSeq, viewerEpoch int64) (assistant.Stream, error)
	Generation(ctx context.Context, member domain.MemberID) (int64, error)
	Fence(ctx context.Context, member domain.MemberID) error
	Unfence(member domain.MemberID)
	Settings(ctx context.Context, member domain.MemberID) (protocol.AssistantSettings, error)
	SetAutoApprove(ctx context.Context, member domain.MemberID, on bool) (protocol.AssistantSettings, error)
}

func init() {
	registerMethod(protocol.MethodAssistantStatus, (*Server).assistantStatus)
	registerMethod(protocol.MethodAssistantStart, (*Server).assistantStart)
	registerGuarded(protocol.MethodAssistantPrompt, permissions.Launch, nil, (*Server).assistantPrompt)
	registerMethod(protocol.MethodAssistantCancel, (*Server).assistantCancel)
	registerMethod(protocol.MethodAssistantAnswer, (*Server).assistantAnswer)
	registerMethod(protocol.MethodAssistantConfirm, (*Server).assistantConfirm)
	registerMethod(protocol.MethodAssistantReset, (*Server).assistantReset)
	registerMethod(protocol.MethodAssistantHistory, (*Server).assistantHistory)
	registerMethod(protocol.MethodAssistantItem, (*Server).assistantItem)
	registerMethod(protocol.MethodAssistantSettings, (*Server).assistantSettings)
}

var errAssistantUnavailable = &protocol.Error{Code: protocol.CodeUnavailable, Message: "the assistant is not configured on this server"}

func (s *Server) assistantStatus(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	status, err := svc.Status(ctx, member)
	if err != nil {
		return nil, assistantError(err)
	}
	return status, nil
}

func (s *Server) assistantStart(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	p, perr := decodeParams[protocol.AssistantStartParams](raw)
	if perr != nil {
		return nil, perr
	}
	status, err := svc.Start(ctx, member, p)
	if err != nil {
		return nil, assistantError(err)
	}
	return status, nil
}

func (s *Server) assistantPrompt(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	p, perr := decodeParams[protocol.AssistantPromptParams](raw)
	if perr != nil {
		return nil, perr
	}
	if strings.TrimSpace(p.Text) == "" || p.IdempotencyKey == "" {
		return nil, invalidParams("text and idempotency_key are required")
	}
	if err := svc.Prompt(ctx, member, p.Text, p.IdempotencyKey); err != nil {
		return nil, assistantError(err)
	}
	return struct{}{}, nil
}

func (s *Server) assistantCancel(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	if err := svc.Cancel(ctx, member); err != nil {
		return nil, assistantError(err)
	}
	return struct{}{}, nil
}

func (s *Server) assistantAnswer(_ context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	p, perr := decodeParams[protocol.AssistantAnswerParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.RequestID == "" || p.OptionID == "" {
		return nil, invalidParams("request_id and option_id are required")
	}
	if err := svc.Answer(member, p.RequestID, p.OptionID, p.Values); err != nil {
		return nil, assistantError(err)
	}
	return struct{}{}, nil
}

func (s *Server) assistantConfirm(_ context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	p, perr := decodeParams[protocol.AssistantConfirmParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.ConfirmationID == "" {
		return nil, invalidParams("confirmation_id is required")
	}
	if err := svc.Confirm(member, p.ConfirmationID, p.Approve); err != nil {
		return nil, assistantError(err)
	}
	return struct{}{}, nil
}

// assistantSettings reads or changes the caller's own assistant settings. The
// assistant's tool socket does not serve it, so the assistant cannot change
// what gates it.
func (s *Server) assistantSettings(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	p, perr := decodeParams[protocol.AssistantSettingsParams](raw)
	if perr != nil {
		return nil, perr
	}
	var settings protocol.AssistantSettings
	var err error
	if p.AutoApprove != nil {
		settings, err = svc.SetAutoApprove(ctx, member, *p.AutoApprove)
	} else {
		settings, err = svc.Settings(ctx, member)
	}
	if err != nil {
		return nil, assistantError(err)
	}
	return settings, nil
}

func (s *Server) assistantReset(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	status, err := svc.Reset(ctx, member)
	if err != nil {
		return nil, assistantError(err)
	}
	return status, nil
}

func (s *Server) assistantHistory(_ context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	p, perr := decodeParams[protocol.AssistantHistoryParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.Limit <= 0 || p.Limit > protocol.ACPHistoryMaxLimit {
		p.Limit = protocol.ACPHistoryMaxLimit
	}
	page, err := svc.History(member, p.BeforeSeq, p.Limit)
	if err != nil {
		return nil, assistantError(err)
	}
	frames := make([]protocol.ACPFrame, len(page.Items))
	for i, it := range page.Items {
		b, truncated, err := it.Wire(protocol.ACPWireItemBytes)
		if err != nil {
			return nil, rpcError(err)
		}
		frames[i] = protocol.ACPFrame{Seq: it.Seq, Item: b, Truncated: truncated}
	}
	return protocol.RunACPHistoryResult{Frames: frames, OldestSeq: page.OldestSeq, TruncatedBefore: page.TruncatedBefore}, nil
}

func (s *Server) assistantItem(_ context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil, errAssistantUnavailable
	}
	p, perr := decodeParams[protocol.AssistantItemParams](raw)
	if perr != nil {
		return nil, perr
	}
	it, err := svc.Item(member, p.Seq)
	if err != nil {
		return nil, assistantError(err)
	}
	b, err := json.Marshal(it)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.RunACPItemResult{Item: b}, nil
}

func (s *Server) fenceAssistant(ctx context.Context, member domain.MemberID) error {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil
	}
	if err := svc.Fence(ctx, member); err != nil {
		return fmt.Errorf("stop the member's assistant: %w", err)
	}
	return nil
}

// removeAssistant stops a removed member's assistant and deletes its files.
func (s *Server) removeAssistant(ctx context.Context, member domain.MemberID) error {
	svc := s.cfg.Services.Assistant
	if svc == nil {
		return nil
	}
	if err := svc.Remove(ctx, member); err != nil {
		return fmt.Errorf("remove the member's assistant: %w", err)
	}
	return nil
}

func assistantError(err error) *protocol.Error {
	var notReady *assistant.AgentNotReadyError
	switch {
	case errors.As(err, &notReady):
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}
	case errors.Is(err, assistant.ErrNoConfirmation):
		return &protocol.Error{Code: protocol.CodeNotFound, Message: err.Error()}
	case errors.Is(err, assistant.ErrTurnRunning):
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	case errors.Is(err, assistant.ErrNotRunning), errors.Is(err, assistant.ErrNoAgent), errors.Is(err, assistant.ErrClosed), errors.Is(err, assistant.ErrRemoving),
		errors.Is(err, scheduler.ErrAssistantNeedsRestart):
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}
	}
	return acpError(err)
}

// serveAssistant serves an aether-assistant channel: one header line, an ack,
// then one protocol.AssistantFrame per line until the session stream ends. A
// member's assistant is theirs alone, so there is no control lease.
func (s *Server) serveAssistant(ctx context.Context, member domain.MemberID, ch subsystemConn) {
	defer func() { _ = ch.Close() }()
	capped := &capReader{r: ch, left: maxSubsystemHeaderBytes}
	r := bufio.NewReaderSize(capped, 4<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1
	var writeMu sync.Mutex
	write := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeJSONLine(ch, v)
	}
	refuse := func(code int, message string) {
		_ = write(protocol.ACPStreamResponse{Code: code, Error: message})
	}
	var req protocol.AssistantStreamRequest
	if err = json.Unmarshal(line, &req); err != nil {
		refuse(protocol.CodeParse, "parse error: "+err.Error())
		return
	}
	if err = s.checkMember(ctx, member); err != nil {
		e := rpcError(err)
		refuse(e.Code, e.Message)
		return
	}
	svc := s.cfg.Services.Assistant
	if svc == nil {
		refuse(errAssistantUnavailable.Code, errAssistantUnavailable.Message)
		return
	}
	stream, err := svc.Subscribe(ctx, member, req.AfterSeq, req.Epoch)
	if err != nil {
		e := assistantError(err)
		refuse(e.Code, e.Message)
		return
	}
	defer stream.Cancel()
	ack := protocol.ACPStreamResponse{
		OK: true, Seq: stream.Seq, Epoch: stream.Epoch, Replay: len(stream.Replay), Live: stream.Items != nil,
		OldestSeq: stream.OldestSeq, TruncatedBefore: stream.TruncatedBefore,
	}
	if stream.State != nil {
		ack.State, _ = json.Marshal(stream.State)
	}
	var lastStatus []byte
	sendStatus := func() error {
		status, err := svc.Status(ctx, member)
		if err != nil {
			status = protocol.AssistantStatus{State: protocol.AssistantStateFailed, Error: err.Error(), Agents: []protocol.AssistantAgent{}, Confirmations: []protocol.AssistantConfirmation{}}
		}
		b, err := json.Marshal(status)
		if err != nil || bytes.Equal(b, lastStatus) {
			return err
		}
		lastStatus = b
		return write(protocol.AssistantFrame{Status: &status})
	}
	if write(ack) != nil || sendStatus() != nil {
		return
	}
	if stream.Reset && write(protocol.ACPFrame{Reset: true, Epoch: stream.Epoch}) != nil {
		return
	}
	item := func(it acphost.Item) error {
		b, truncated, err := it.Wire(protocol.ACPWireItemBytes)
		if err != nil {
			return err
		}
		return write(protocol.AssistantFrame{ACPFrame: protocol.ACPFrame{Seq: it.Seq, Item: b, Truncated: truncated}})
	}
	for _, it := range stream.Replay {
		if item(it) != nil {
			return
		}
	}

	ctx, revoke := context.WithCancelCause(ctx)
	defer revoke(nil)
	s.spawn(func() {
		defer revoke(nil)
		_, _ = r.Peek(1)
	})
	s.spawn(func() { s.watchAssistantMembership(ctx, member, revoke) })

	status := 0
	items := stream.Items
	for status == 0 {
		select {
		case <-ctx.Done():
			status = 1
		case <-stream.Started:
			status = 1
		case <-stream.Updates:
			if sendStatus() != nil {
				status = 1
			}
		case it, ok := <-items:
			if !ok {
				status = 1
			} else if item(it) != nil {
				status = 1
			}
		}
	}
	if errors.Is(context.Cause(ctx), errAttachMembershipRevoked) {
		ch.exit(protocol.AttachExitMembershipRevoked)
		return
	}
	// A reset ends the stream like any session end; tell the viewer which.
	if generation, err := svc.Generation(context.WithoutCancel(ctx), member); err == nil && generation != stream.Generation {
		_ = write(protocol.ACPFrame{Reset: true, Epoch: assistant.Epoch(generation, 0)})
	}
	ch.exit(0)
}

func (s *Server) watchAssistantMembership(ctx context.Context, member domain.MemberID, revoke context.CancelCauseFunc) {
	interval := s.cfg.revalidateInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.checkMember(ctx, member) != nil {
				revoke(errAttachMembershipRevoked)
				return
			}
		}
	}
}
