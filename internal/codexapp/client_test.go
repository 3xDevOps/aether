package codexapp_test

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/codexapp"
	"github.com/3xDevOps/Aether/internal/codexapp/codexmock"
)

type statusChange struct {
	thread string
	status codexapp.Status
}

func dial(t *testing.T, srv *codexmock.Server, session string) (*codexapp.Client, <-chan statusChange) {
	t.Helper()
	toServer, clientOut := io.Pipe()
	clientIn, fromServer := io.Pipe()
	go srv.Serve(toServer, fromServer)
	changes := make(chan statusChange, 16)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, err := codexapp.Dial(ctx, clientIn, clientOut, codexapp.Config{
		Session:  session,
		OnStatus: func(thread string, status codexapp.Status) { changes <- statusChange{thread, status} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(); _ = fromServer.Close() })
	return c, changes
}

func next(t *testing.T, changes <-chan statusChange) statusChange {
	t.Helper()
	select {
	case c := <-changes:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no status change")
		return statusChange{}
	}
}

func TestClientReportsTheTerminalThreadOnly(t *testing.T) {
	srv := codexmock.New()
	c, changes := dial(t, srv, "")
	srv.StartThread("title", true, "")
	srv.StartThread("main", false, "")
	srv.StartThread("subagent", false, "main")
	srv.SetStatus("title", "active")
	srv.SetStatus("subagent", "active")
	srv.SetStatus("main", "active")
	srv.SetStatus("main", "active", "waitingOnUserInput")
	srv.SetStatus("main", "systemError")
	srv.SetStatus("main", "idle")
	for _, want := range []statusChange{{"main", codexapp.Working}, {"main", codexapp.Asking}, {"main", codexapp.Failed}, {"main", codexapp.Idle}} {
		if got := next(t, changes); got != want {
			t.Fatalf("status change = %+v, want %+v", got, want)
		}
	}
	if got := c.Thread(); got != "main" {
		t.Fatalf("Thread() = %q, want main", got)
	}
}

// A new thread in the terminal keeps the client on it while the old one goes
// idle and unloads.
func TestClientStaysOnTheTerminalsNewThread(t *testing.T) {
	srv := codexmock.New()
	c, changes := dial(t, srv, "")
	srv.StartThread("first", false, "")
	srv.SetStatus("first", "active")
	next(t, changes)
	srv.StartThread("second", false, "")
	srv.SetStatus("first", "idle")
	srv.SetStatus("second", "active")
	if got := next(t, changes); got != (statusChange{"second", codexapp.Working}) {
		t.Fatalf("status change = %+v, want the new thread working", got)
	}
	if got := c.Thread(); got != "second" {
		t.Fatalf("Thread() = %q, want second", got)
	}
}

// A terminal that resumes another conversation while idle moves the client
// to it, and back again to one it left and unloaded.
func TestClientFollowsAConversationTheTerminalResumes(t *testing.T) {
	srv := codexmock.New()
	c, changes := dial(t, srv, "")
	srv.StartThread("first", false, "")
	srv.SetStatus("first", "idle")
	next(t, changes)
	srv.LoadThread("saved", false, "", 0)
	srv.SetStatus("saved", "idle")
	srv.SetStatus("first", "notLoaded")
	if got := next(t, changes); got != (statusChange{"saved", codexapp.Idle}) {
		t.Fatalf("status change = %+v, want the resumed conversation", got)
	}
	srv.SetStatus("first", "idle")
	if got := next(t, changes); got != (statusChange{"first", codexapp.Idle}) {
		t.Fatalf("status change = %+v, want the conversation resumed again", got)
	}
	if got := c.Thread(); got != "first" {
		t.Fatalf("Thread() = %q, want first", got)
	}
}

// A terminal that resumes a session loads its thread without announcing it.
func TestClientFollowsAResumedThread(t *testing.T) {
	srv := codexmock.New()
	c, changes := dial(t, srv, "")
	srv.LoadThread("resumed", false, "", 0)
	srv.SetStatus("resumed", "idle")
	if got := next(t, changes); got != (statusChange{"resumed", codexapp.Idle}) {
		t.Fatalf("status change = %+v", got)
	}
	if got := c.Thread(); got != "resumed" {
		t.Fatalf("Thread() = %q, want resumed", got)
	}
}

func TestDialFindsTheLoadedTerminalThread(t *testing.T) {
	for _, tc := range []struct{ session, want string }{{"", "newer"}, {"older", "older"}} {
		srv := codexmock.New()
		srv.LoadThread("title", true, "", 9)
		srv.LoadThread("older", false, "", 1)
		srv.LoadThread("newer", false, "", 2)
		c, _ := dial(t, srv, tc.session)
		if got := c.Thread(); got != tc.want {
			t.Errorf("session %q: Thread() = %q, want %q", tc.session, got, tc.want)
		}
	}
}

func TestSendStartsATurnOrSteersTheRunningOne(t *testing.T) {
	srv := codexmock.New()
	c, _ := dial(t, srv, "")
	ctx := t.Context()
	if err := c.Send(ctx, "before any thread"); err == nil {
		t.Fatal("Send before the terminal opened a thread succeeded")
	}
	srv.StartThread("main", false, "")
	waitThread(t, c)
	for _, text := range []string{"first", "mid-turn"} {
		if err := c.Send(ctx, text); err != nil {
			t.Fatalf("Send(%q): %v", text, err)
		}
	}
	srv.EndTurn("main")
	if err := c.Send(ctx, "next"); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, srv.Requests("turn/start")); !slices.Equal(got, []string{"first", "next"}) {
		t.Fatalf("turn/start inputs = %q", got)
	}
	steers := srv.Requests("turn/steer")
	if got := texts(t, steers); !slices.Equal(got, []string{"mid-turn"}) {
		t.Fatalf("turn/steer inputs = %q", got)
	}
	var steer struct {
		ExpectedTurnID string `json:"expectedTurnId"`
	}
	if err := json.Unmarshal(steers[0], &steer); err != nil || steer.ExpectedTurnID != "turn-1" {
		t.Fatalf("steer = %s, want expectedTurnId turn-1", steers[0])
	}
}

// A turn that ends between the turn lookup and the steer takes the text as
// a new turn.
func TestSendStartsATurnWhenTheSteerMisses(t *testing.T) {
	srv := codexmock.New()
	c, _ := dial(t, srv, "")
	srv.StartThread("main", false, "")
	waitThread(t, c)
	if err := c.Send(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}
	srv.FailSteers()
	if err := c.Send(t.Context(), "late"); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, srv.Requests("turn/start")); !slices.Equal(got, []string{"first", "late"}) {
		t.Fatalf("turn/start inputs = %q", got)
	}
}

func waitThread(t *testing.T, c *codexapp.Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.Thread() == "" {
		if time.Now().After(deadline) {
			t.Fatal("the client never followed the started thread")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func texts(t *testing.T, params []json.RawMessage) []string {
	t.Helper()
	var out []string
	for _, raw := range params {
		var p struct {
			Input []struct {
				Text string `json:"text"`
			} `json:"input"`
		}
		if err := json.Unmarshal(raw, &p); err != nil || len(p.Input) != 1 {
			t.Fatalf("params = %s", raw)
		}
		out = append(out, p.Input[0].Text)
	}
	return out
}
