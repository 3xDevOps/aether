package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/coder/websocket"
)

const (
	retryMin = time.Second
	retryMax = time.Minute
	// maxEnvelope bounds one Socket Mode message; an interaction carries
	// the whole message its button is on.
	maxEnvelope = 1 << 20
	// handleTimeout bounds acting on one event, inside which every call
	// to Slack has its own callTimeout.
	handleTimeout = time.Minute
	// A full inbox holds back the next acknowledgement, so Slack resends
	// that envelope; an event id or a request's answer makes the resend
	// harmless.
	inboxSize = 64
)

// envelope is one Socket Mode message. Reason is a disconnect's.
type envelope struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id"`
	Payload    json.RawMessage `json:"payload"`
	Reason     string          `json:"reason"`
}

// run connects until ctx ends: again at once after a connection Slack
// said hello on, as when it asks for a refresh, and after a growing wait
// once one failed before.
func (s *Service) run(ctx context.Context) {
	var delay time.Duration
	for {
		hello, err := s.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		s.state(false, err)
		slog.Warn("slack: disconnected", "error", err)
		delay = min(max(2*delay, retryMin), retryMax)
		if hello {
			delay = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// connect opens one Socket Mode connection and serves it until it ends,
// and reports whether Slack said hello on it.
func (s *Service) connect(ctx context.Context) (bool, error) {
	if s.identity().UserID == "" {
		var self identity
		if err := s.api.call(ctx, s.cfg.BotToken, "auth.test", nil, &self); err != nil {
			return false, err
		}
		s.mu.Lock()
		s.self = self
		s.mu.Unlock()
	}
	var open struct {
		URL string `json:"url"`
	}
	if err := s.api.call(ctx, s.cfg.AppToken, "apps.connections.open", nil, &open); err != nil {
		return false, err
	}
	conn, _, err := websocket.Dial(ctx, open.URL, &websocket.DialOptions{HTTPClient: s.cfg.HTTP})
	if err != nil {
		// The URL's ticket opens a connection; status and logs show the
		// cause without it.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return false, fmt.Errorf("slack: connect to Socket Mode: %w", err)
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxEnvelope)
	hello := false
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return hello, fmt.Errorf("slack: Socket Mode: %w", err)
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return hello, fmt.Errorf("slack: Socket Mode: decode: %w", err)
		}
		switch env.Type {
		case "hello":
			hello = true
			s.state(true, nil)
			continue
		case "disconnect":
			return hello, fmt.Errorf("slack: Socket Mode: Slack closed the connection (%s)", env.Reason)
		}
		if env.EnvelopeID == "" {
			continue
		}
		// Slack resends an envelope not acknowledged within 3 seconds, so
		// it is acknowledged before it is acted on.
		ack, _ := json.Marshal(struct {
			EnvelopeID string `json:"envelope_id"`
		}{env.EnvelopeID})
		if err := conn.Write(ctx, websocket.MessageText, ack); err != nil {
			return hello, fmt.Errorf("slack: Socket Mode: acknowledge: %w", err)
		}
		select {
		case s.inbox <- env:
		case <-ctx.Done():
			return hello, ctx.Err()
		}
	}
}

// work acts on envelopes one at a time in the order Slack sent them, so
// replies reach a run in the order they were written.
func (s *Service) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case env := <-s.inbox:
			handleCtx, cancel := context.WithTimeout(ctx, handleTimeout)
			s.handle(handleCtx, env)
			cancel()
		}
	}
}

func (s *Service) handle(ctx context.Context, env envelope) {
	var err error
	switch env.Type {
	case "events_api":
		var cb eventCallback
		if err = json.Unmarshal(env.Payload, &cb); err == nil {
			s.event(ctx, cb)
		}
	case "interactive":
		var in interaction
		if err = json.Unmarshal(env.Payload, &in); err == nil {
			s.interact(ctx, in)
		}
	}
	logged("decode "+env.Type, err)
}
