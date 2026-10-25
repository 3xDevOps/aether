// Package codexapp runs a Standard Codex terminal attached to Codex's
// app-server, and is the server's JSON-RPC client of that app-server.
package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/version"
)

// sendTimeout bounds a message's turn request once it is sent.
const sendTimeout = 15 * time.Second

// Status is what the app-server reports of the terminal's thread.
type Status int

const (
	Idle Status = iota + 1
	Working
	// Asking: the turn waits for an answer the person gives in the terminal.
	Asking
	// Failed: the app-server reports a system error on the thread.
	Failed
)

// Config is optional; a zero Config follows the terminal's thread without
// reporting it.
type Config struct {
	// Session is the thread to follow if the app-server has it loaded, the
	// run's recorded session.
	Session string
	// OnStatus receives the followed thread on every change of its status.
	// It runs on the connection's notification loop and must not block.
	OnStatus func(thread string, status Status)
}

// Client follows the main thread of the terminal attached to the same
// app-server: not an ephemeral thread, such as a title generation, and not
// a subagent's. It never subscribes to the thread, so it receives only the
// status changes the app-server broadcasts.
type Client struct {
	conn  *acp.Connection
	input io.Closer
	cfg   Config

	mu sync.Mutex
	// main is the followed thread; threads classifies every other thread
	// seen, true for a terminal thread.
	main    string
	status  Status
	threads map[string]bool
	// selections counts follows, so a read that returns after a newer one
	// does not undo it.
	selections uint64
	// unloaded marks threads the app-server unloaded, whose next load is the
	// terminal resuming them.
	unloaded map[string]bool
	// unread holds the last status of a thread being classified.
	unread map[string]threadStatus
}

type thread struct {
	ID             string        `json:"id"`
	Ephemeral      bool          `json:"ephemeral"`
	ParentThreadID *string       `json:"parentThreadId"`
	UpdatedAt      int64         `json:"updatedAt"`
	Status         *threadStatus `json:"status"`
}

func (t thread) terminal() bool { return !t.Ephemeral && t.ParentThreadID == nil }

type threadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags"`
}

func (s threadStatus) status() Status {
	switch s.Type {
	case "idle":
		return Idle
	case "systemError":
		return Failed
	case "active":
		if slices.Contains(s.ActiveFlags, "waitingOnUserInput") || slices.Contains(s.ActiveFlags, "waitingOnApproval") {
			return Asking
		}
		return Working
	}
	return 0
}

type textInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Dial initializes a JSON-RPC session with the app-server behind r and w and
// finds the terminal's thread among those already loaded. Closing the
// client closes w.
func Dial(ctx context.Context, r io.Reader, w io.WriteCloser, cfg Config) (*Client, error) {
	c := &Client{input: w, cfg: cfg, threads: make(map[string]bool), unread: make(map[string]threadStatus), unloaded: make(map[string]bool)}
	c.conn = acp.NewConnection(c.handle, w, r)
	info := map[string]any{"clientInfo": map[string]string{"name": "aether", "title": "Aether", "version": version.String()}}
	if _, err := acp.SendRequest[json.RawMessage](c.conn, ctx, "initialize", info); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("codex app-server: initialize: %w", err)
	}
	if err := c.conn.SendNotification(ctx, "initialized", nil); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("codex app-server: initialized: %w", err)
	}
	loaded, err := acp.SendRequest[struct {
		Data []string `json:"data"`
	}](c.conn, ctx, "thread/loaded/list", struct{}{})
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("codex app-server: list loaded threads: %w", err)
	}
	// The recorded session wins; else the most recently updated thread.
	var found *thread
	for _, id := range loaded.Data {
		t, err := c.read(ctx, id)
		if err != nil {
			_ = w.Close()
			return nil, err
		}
		c.mu.Lock()
		c.threads[id] = t.terminal()
		c.mu.Unlock()
		if t.terminal() && (found == nil || t.ID == cfg.Session || found.ID != cfg.Session && t.UpdatedAt > found.UpdatedAt) {
			found = &t
		}
	}
	c.mu.Lock()
	if found != nil && c.main == "" {
		c.follow(found.ID)
		if found.Status != nil {
			c.setStatus(*found.Status)
		}
	}
	c.mu.Unlock()
	return c, nil
}

func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

func (c *Client) Close() error { return c.input.Close() }

// Thread is the followed thread, empty until the terminal opens one.
func (c *Client) Thread() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.main
}

// Busy reports the followed thread in a turn.
func (c *Client) Busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status == Working || c.status == Asking
}

// Send hands text to the terminal's thread: it steers the running turn, or
// starts a turn when none runs. Either shows in the terminal.
func (c *Client) Send(ctx context.Context, text string) error {
	thread := c.Thread()
	if thread == "" {
		return errors.New("codex app-server: the terminal has not opened a thread yet")
	}
	turn := c.runningTurn(ctx, thread)
	if err := ctx.Err(); err != nil {
		return err
	}
	// The app-server may act on a request whose answer the caller does not
	// wait for, and a delivered message must not read as failed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
	defer cancel()
	input := []textInput{{Type: "text", Text: text}}
	if turn != "" {
		_, err := acp.SendRequest[json.RawMessage](c.conn, ctx, "turn/steer", map[string]any{"threadId": thread, "expectedTurnId": turn, "input": input})
		if err == nil {
			return nil
		}
		// The turn ended before the steer reached it.
	}
	if _, err := acp.SendRequest[json.RawMessage](c.conn, ctx, "turn/start", map[string]any{"threadId": thread, "input": input}); err != nil {
		return fmt.Errorf("codex app-server: start a turn: %w", err)
	}
	return nil
}

// runningTurn is empty when the thread has no turn in progress, or none yet:
// the app-server lists no turns before the first message.
func (c *Client) runningTurn(ctx context.Context, thread string) string {
	turns, err := acp.SendRequest[struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}](c.conn, ctx, "thread/turns/list", map[string]any{"threadId": thread, "limit": 1, "itemsView": "notLoaded"})
	if err != nil || len(turns.Data) == 0 || turns.Data[0].Status != "inProgress" {
		return ""
	}
	return turns.Data[0].ID
}

func (c *Client) read(ctx context.Context, id string) (thread, error) {
	res, err := acp.SendRequest[struct {
		Thread thread `json:"thread"`
	}](c.conn, ctx, "thread/read", map[string]any{"threadId": id})
	if err != nil {
		return thread{}, fmt.Errorf("codex app-server: read thread %s: %w", id, err)
	}
	return res.Thread, nil
}

// handle runs on the SDK's notification loop, which a request issued from it
// would wait on, so classification reads run beside it.
func (c *Client) handle(_ context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	switch method {
	case "thread/started":
		var p struct {
			Thread thread `json:"thread"`
		}
		if json.Unmarshal(params, &p) == nil && p.Thread.ID != "" {
			c.mu.Lock()
			c.threads[p.Thread.ID] = p.Thread.terminal()
			if p.Thread.terminal() {
				c.follow(p.Thread.ID)
			}
			c.mu.Unlock()
		}
	case "thread/status/changed":
		var p struct {
			ThreadID string       `json:"threadId"`
			Status   threadStatus `json:"status"`
		}
		if json.Unmarshal(params, &p) == nil && p.ThreadID != "" {
			c.changed(p.ThreadID, p.Status)
		}
	}
	// Notifications take no answer. The app-server asks only the client that
	// started a turn, and Aether's turns run without approvals or questions,
	// so no request needs one either.
	return nil, nil
}

// changed follows a thread the client has not seen start, such as one the
// terminal resumed, once a read shows it is a terminal thread. A known one
// is followed when work starts there or it loads again: the thread the
// terminal left goes idle and unloads.
func (c *Client) changed(id string, s threadStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	terminal, known := c.threads[id]
	resumed := c.unloaded[id] && s.Type != "notLoaded"
	if s.Type == "notLoaded" {
		c.unloaded[id] = true
	} else {
		delete(c.unloaded, id)
	}
	switch {
	case id == c.main:
		c.setStatus(s)
	case known:
		if terminal && (s.Type == "active" || resumed) {
			c.follow(id)
			c.setStatus(s)
		}
	default:
		_, reading := c.unread[id]
		c.unread[id] = s
		if !reading {
			go c.classify(id)
		}
	}
}

func (c *Client) classify(id string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-c.conn.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	c.mu.Lock()
	selections := c.selections
	c.mu.Unlock()
	t, err := c.read(ctx, id)
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.unread[id]
	delete(c.unread, id)
	if err != nil {
		return
	}
	c.threads[id] = t.terminal()
	if t.terminal() && c.selections == selections {
		c.follow(id)
		c.setStatus(s)
	}
}

// follow and setStatus run under c.mu.
func (c *Client) follow(id string) {
	if id == c.main {
		return
	}
	c.main, c.status = id, 0
	c.threads[id] = true
	c.selections++
}

func (c *Client) setStatus(s threadStatus) {
	status := s.status()
	if status == 0 || status == c.status {
		return
	}
	c.status = status
	if c.cfg.OnStatus != nil {
		c.cfg.OnStatus(c.main, status)
	}
}
