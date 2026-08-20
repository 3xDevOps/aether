package mcpbridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// These tests compose the real thing: a real coord.Service over a real
// SQLite mailbox, its real unix sockets, the bridge, and a real MCP client
// from the SDK. The golden-fixture tests prove the bridge puts the right
// bytes on the wire; only this proves the acknowledgement protocol behaves
// against the service that actually implements the other half of it.
//
// No Docker and no git, so it runs with the unit tests rather than behind
// the integration tag - the scheduler tests set the same precedent with a
// real store and a real bus.

const overlapFile = "src/auth.go"

// livePeers is the conflict radar's read side: two runs permanently in
// conflict, which is what authorizes them to message each other.
type livePeers struct{ a, b domain.RunID }

func (p livePeers) Overlaps(context.Context) ([]overlap.Entry, error) {
	return []overlap.Entry{
		{RunID: p.a, With: []overlap.Peer{{RunID: p.b, Files: []string{overlapFile}}}},
		{RunID: p.b, With: []overlap.Peer{{RunID: p.a, Files: []string{overlapFile}}}},
	}, nil
}

type coordStack struct {
	svc        *coord.Service
	bus        *events.InProc
	session    domain.SessionID
	runA, runB domain.RunID
	sockA      string
	sockB      string
}

func newCoordStack(t *testing.T) *coordStack {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()

	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	ws := &domain.Workspace{Name: "proj", Environment: domain.WorkspaceEnvironment{CustomImage: "img"}}
	if werr := db.CreateWorkspace(ctx, ws); werr != nil {
		t.Fatalf("create workspace: %v", werr)
	}
	ses := &domain.Session{WorkspaceID: ws.ID, Name: "auth", BaseBranch: "main"}
	if serr := db.CreateSession(ctx, ses); serr != nil {
		t.Fatalf("create session: %v", serr)
	}
	mem := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example.com", Color: "#e6194b", Role: domain.RoleCollaborator}
	if merr := db.CreateMember(ctx, mem); merr != nil {
		t.Fatalf("create member: %v", merr)
	}
	runs := make([]domain.RunID, 2)
	for i := range runs {
		r := &domain.Run{
			SessionID: ses.ID,
			MemberID:  mem.ID,
			Task:      fmt.Sprintf("task %d", i),
			Harness:   "claude",
			Mode:      domain.LaunchTUI,
			Status:    domain.RunRunning,
		}
		if rerr := db.CreateRun(ctx, r); rerr != nil {
			t.Fatalf("create run %d: %v", i, rerr)
		}
		runs[i] = r.ID
	}

	svc, err := coord.New(coord.Config{
		Dir:   filepath.Join(dir, "coord"),
		Store: db,
		Mail:  db,
		Bus:   bus,
		Peers: livePeers{a: runs[0], b: runs[1]},
	})
	if err != nil {
		t.Fatalf("coord.New: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if serr := svc.Start(ctx); serr != nil {
		t.Fatalf("coord.Start: %v", serr)
	}

	s := &coordStack{svc: svc, bus: bus, session: ses.ID, runA: runs[0], runB: runs[1]}
	for i, run := range runs {
		provisioned, perr := svc.Provision(ctx, run, nil)
		if perr != nil {
			t.Fatalf("provision %s: %v", run, perr)
		}
		sock := filepath.Join(provisioned, coord.SocketName)
		if i == 0 {
			s.sockA = sock
		} else {
			s.sockB = sock
		}
	}
	return s
}

// TestBridgeAgainstRealCoordination drives the spec's integration case end
// to end: two agents, each on its own bridge, exchanging a message over
// real sockets, with the send landing in the session timeline.
func TestBridgeAgainstRealCoordination(t *testing.T) {
	stack := newCoordStack(t)
	sub, err := stack.bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Session: stack.session},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	agentA := session(t, stack.sockA, nil)
	agentB := session(t, stack.sockB, nil)

	// B sees exactly the peer it may message, and messages it.
	var statusB protocol.CoordStatusResult
	callTool(t, agentB, toolStatus, nil, &statusB)
	if statusB.WireVersion != protocol.CoordWireVersion || statusB.RunID != string(stack.runB) {
		t.Fatalf("status = %+v", statusB)
	}
	if len(statusB.Peers) != 1 || statusB.Peers[0].RunID != string(stack.runA) ||
		statusB.Peers[0].State != protocol.CoordPeerActive {
		t.Fatalf("peers = %+v, want run A active", statusB.Peers)
	}

	const body = "only adding an import - going ahead"
	var sent protocol.CoordSendResult
	callTool(t, agentB, toolSend, protocol.CoordSendParams{ToRunID: string(stack.runA), Body: body}, &sent)
	if sent.MessageID == "" {
		t.Fatal("send returned no message id")
	}

	// A is told it has mail, then drains it.
	var statusA protocol.CoordStatusResult
	callTool(t, agentA, toolStatus, nil, &statusA)
	if statusA.Unread != 1 {
		t.Fatalf("unread = %d, want 1", statusA.Unread)
	}
	var inbox inboxOutput
	callTool(t, agentA, toolInbox, nil, &inbox)
	if len(inbox.Messages) != 1 || inbox.Messages[0].Body != body ||
		inbox.Messages[0].FromRunID != string(stack.runB) {
		t.Fatalf("inbox = %+v", inbox)
	}

	// The next read presents the token the bridge held back, so the service
	// retires that batch and A's inbox is empty.
	callTool(t, agentA, toolInbox, nil, &inbox)
	if len(inbox.Messages) != 0 {
		t.Fatalf("second inbox = %+v, want empty", inbox)
	}

	waitForTimeline(t, sub, body)
}

// TestRealBatchRedeliversWhenTheResponseIsLost is the crash the whole
// acknowledgement dance exists for, against the service that implements
// the other half: the socket has handed the batch over when the bridge dies
// before the agent sees it. Nothing was acknowledged, so it comes back.
func TestRealBatchRedeliversWhenTheResponseIsLost(t *testing.T) {
	stack := newCoordStack(t)
	const body = "I'm rewriting login(); done in ~10 min."

	agentB := session(t, stack.sockB, nil)
	var sent protocol.CoordSendResult
	callTool(t, agentB, toolSend, protocol.CoordSendParams{ToRunID: string(stack.runA), Body: body}, &sent)

	// The only message that can carry the body is the inbox response, so
	// failing on it drops exactly the response and nothing else.
	dying := session(t, stack.sockA, func(dst io.WriteCloser) io.WriteCloser {
		return &brokenWriter{dst: dst, fail: func(p []byte) bool {
			return bytes.Contains(p, []byte(body))
		}}
	})
	if _, err := dying.CallTool(t.Context(), &mcp.CallToolParams{Name: toolInbox}); err == nil {
		t.Fatal("inbox succeeded even though its response could not be written")
	}

	revived := session(t, stack.sockA, nil)
	var inbox inboxOutput
	callTool(t, revived, toolInbox, nil, &inbox)
	if len(inbox.Messages) != 1 || inbox.Messages[0].Body != body {
		t.Fatalf("redelivered inbox = %+v, want the same batch", inbox)
	}
	// And the redelivery is not endless: this read did reach the agent, so
	// the next one retires it.
	callTool(t, revived, toolInbox, nil, &inbox)
	if len(inbox.Messages) != 0 {
		t.Fatalf("inbox after a delivered read = %+v, want empty", inbox)
	}
}

// TestRealCancelledInboxNeverLosesAMessage drives the sequence the ticket
// calls out - send, a cancelled inbox, then a real one - against the live
// service.
//
// The assertion is the at-least-once property itself rather than "the
// second read returns it", because both orderings are legal: the client may
// have received the cancelled call's response before it gave up, in which
// case that batch is genuinely delivered and correctly acknowledged. What
// must never happen, in any ordering, is the message reaching nobody.
func TestRealCancelledInboxNeverLosesAMessage(t *testing.T) {
	stack := newCoordStack(t)
	const body = "you finish first, I'll wait"

	agentB := session(t, stack.sockB, nil)
	var sent protocol.CoordSendResult
	callTool(t, agentB, toolSend, protocol.CoordSendParams{ToRunID: string(stack.runA), Body: body}, &sent)

	agentA := session(t, stack.sockA, nil)
	delivered := 0

	abandoned, cancel := context.WithCancel(t.Context())
	cancel()
	if res, err := agentA.CallTool(abandoned, &mcp.CallToolParams{Name: toolInbox}); err == nil {
		delivered += messageCount(t, res)
	}

	var inbox inboxOutput
	callTool(t, agentA, toolInbox, nil, &inbox)
	delivered += len(inbox.Messages)

	if delivered == 0 {
		t.Fatal("a peer's message was acknowledged without ever reaching the agent")
	}
}

func messageCount(t *testing.T, res *mcp.CallToolResult) int {
	t.Helper()
	if res.IsError {
		return 0
	}
	var out inboxOutput
	decodeStructured(t, res, &out)
	return len(out.Messages)
}

// waitForTimeline waits for the timeline entry the service stamps every
// message with, so the audit trail is part of the round trip rather than an
// assumption about it.
func waitForTimeline(t *testing.T, sub events.Subscription, body string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatal("event stream closed before the message reached the timeline")
			}
			if p, isTL := ev.Payload.(events.TimelinePayload); isTL && bytes.Contains([]byte(p.Message), []byte(body)) {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the message to reach the session timeline")
		}
	}
}
