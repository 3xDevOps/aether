package mcpbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// session runs a bridge against sock and returns a connected real MCP
// client session. wrap, when non-nil, interposes on the bridge's output
// stream.
func session(t *testing.T, sock string, wrap func(io.WriteCloser) io.WriteCloser) *mcp.ClientSession {
	t.Helper()
	toBridge, fromClient := io.Pipe()
	toClient, fromBridge := io.Pipe()
	var out io.WriteCloser = fromBridge
	if wrap != nil {
		out = wrap(fromBridge)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(t.Context(), Config{Socket: sock, In: toBridge, Out: out})
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "v0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: toClient, Writer: fromClient}, nil)
	if err != nil {
		t.Fatalf("connect to bridge: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = fromClient.Close()
		_ = toClient.Close()
		<-done
	})
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s reported a tool error: %+v", name, res.Content)
	}
	if out != nil {
		decodeStructured(t, res, out)
	}
	return res
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("re-marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
}

// TestBridgeSpeaksGoldenWireV3 drives a real MCP client through every v3
// tool against a coordination socket that answers with pinned wire bytes.
func TestBridgeSpeaksGoldenWireV3(t *testing.T) {
	success, errorResponses := golden(t, "success.ndjson"), golden(t, "errors.ndjson")
	requests := goldenRequests(t)

	const ack = "01k1h7m4z9q0r8s5t2v6w3x7y1"
	var mu sync.Mutex
	var retired, queued bool
	coord := newFakeCoord(t, func(req protocol.Request) protocol.Response {
		mu.Lock()
		defer mu.Unlock()
		switch req.Method {
		case protocol.MethodCoordStatus:
			return success[0]
		case protocol.MethodCoordSend:
			var p protocol.CoordSendParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Errorf("decode send params: %v", err)
			}
			if p.ToRunID == "run_09" {
				return errorResponses[0]
			}
			return success[1]
		case protocol.MethodCoordAsk:
			return success[2]
		case protocol.MethodCoordReply:
			return success[3]
		case protocol.MethodCoordInbox:
			var p protocol.CoordInboxParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Errorf("decode inbox params: %v", err)
			}
			// Like the real mailbox, a token retires its batch once and
			// replaying it is a no-op: the bridge promotes a token only
			// after the client has already read the response, so the read
			// after the acknowledgement may still carry the old token.
			if p.AckToken == ack {
				retired = true
			}
			if retired && !queued {
				return protocol.Response{Result: json.RawMessage(`{"messages":[]}`)}
			}
			return success[4]
		case protocol.MethodCoordReport:
			return success[5]
		}
		t.Errorf("bridge called %q, which is not on the coordination wire", req.Method)
		return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
	})

	cs := session(t, coord.path, nil)

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	wantTools := []string{toolAsk, toolInbox, toolReply, toolReport, toolSend, toolStatus}
	if !slices.Equal(names, wantTools) {
		t.Fatalf("tools = %v, want exactly %v", names, wantTools)
	}

	var status protocol.CoordStatusResult
	callTool(t, cs, toolStatus, nil, &status)
	if status.WireVersion != protocol.CoordWireVersion || status.RunID != "run_01" || status.Unread != 1 {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Peers) != 2 ||
		status.Peers[0].State != protocol.CoordPeerActive || !slices.Equal(status.Peers[0].Files, []string{"src/auth.go"}) ||
		status.Peers[1].State != protocol.CoordPeerGrace || status.Peers[1].ExpiresAt == "" {
		t.Fatalf("status peers = %+v", status.Peers)
	}

	for _, peer := range status.Peers {
		var sent protocol.CoordSendResult
		callTool(t, cs, toolSend, protocol.CoordSendParams{
			ToRunID:        peer.RunID,
			Body:           "I'm rewriting login(); done in ~10 min.",
			IdempotencyKey: "send-01",
		}, &sent)
		if sent.MessageID != "msg_01" {
			t.Fatalf("send to %s = %+v", peer.RunID, sent)
		}
	}
	var question protocol.CoordAskResult
	callTool(t, cs, toolAsk, protocol.CoordAskParams{
		ToRunID:        "run_02",
		Body:           "Can I change the auth helper?",
		IdempotencyKey: "ask-01",
	}, &question)
	if question.QuestionID != "q_01" {
		t.Fatalf("ask = %+v", question)
	}
	var reply protocol.CoordReplyResult
	callTool(t, cs, toolReply, protocol.CoordReplyParams{
		QuestionID:     question.QuestionID,
		Body:           "Yes, go ahead.",
		IdempotencyKey: "reply-01",
	}, &reply)
	if reply.MessageID != "msg_03" {
		t.Fatalf("reply = %+v", reply)
	}

	denied, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      toolSend,
		Arguments: protocol.CoordSendParams{ToRunID: "run_09", Body: "hello"},
	})
	if err != nil {
		t.Fatalf("send to a non-peer: %v", err)
	}
	if code := errorCode(t, denied); code != protocol.CodeDenied {
		t.Fatalf("send to a non-peer: code = %d, want %d", code, protocol.CodeDenied)
	}

	var inbox inboxOutput
	callTool(t, cs, toolInbox, nil, &inbox)
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != "msg_02" || inbox.AckToken != ack {
		t.Fatalf("inbox = %+v", inbox)
	}
	inboxes := coord.readUntilAcked(t, cs, ack, &inbox)
	if len(inboxes) < 2 {
		t.Fatalf("coordination saw %d inbox calls, want at least 2", len(inboxes))
	}
	var acking protocol.CoordInboxParams
	if err := json.Unmarshal(inboxes[len(inboxes)-1].Params, &acking); err != nil {
		t.Fatalf("decode acknowledging inbox params: %v", err)
	}
	if acking.AckToken != ack {
		t.Fatalf("acknowledging inbox params = %+v, want token %q", acking, ack)
	}
	mu.Lock()
	queued = true
	mu.Unlock()
	callTool(t, cs, toolInbox, nil, &inbox)
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != "msg_02" {
		t.Fatalf("inbox after acknowledging the first batch = %+v, want the later queued question to remain", inbox)
	}

	var report protocol.CoordReportResult
	callTool(t, cs, toolReport, protocol.CoordReportParams{
		Outcome:        protocol.CoordOutcomeSuccess,
		Summary:        "Implemented and verified.",
		EvidenceRefs:   []string{"ev_01"},
		IdempotencyKey: "report-01",
	}, &report)
	if report.ReportID != "report_01" {
		t.Fatalf("report = %+v", report)
	}

	seen := coord.seen()
	methods := make(map[string]bool, len(seen))
	for _, req := range seen {
		methods[req.Method] = true
	}
	for _, want := range requests {
		if !methods[want.Method] {
			t.Fatalf("coordination did not receive %q; requests = %+v", want.Method, seen)
		}
	}
}

// TestBatchRedeliversWhenTheResponseNeverLands is the crash the at-least-once
// contract exists for: the coordination socket has already handed over a
// batch when the bridge dies before the agent sees it. Nothing was
// acknowledged, so the next bridge asks again with no token and the same
// batch comes back.
func TestBatchRedeliversWhenTheResponseNeverLands(t *testing.T) {
	success := golden(t, "success.ndjson")
	batch := success[4]

	var mu sync.Mutex
	var armed bool
	coord := newFakeCoord(t, func(req protocol.Request) protocol.Response {
		if req.Method != protocol.MethodCoordInbox {
			return success[0]
		}
		mu.Lock()
		defer mu.Unlock()
		var p protocol.CoordInboxParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeInvalidParams}}
		}
		// Unacknowledged batches redeliver: only a read carrying the
		// token retires one.
		if p.AckToken == "" {
			armed = true
			return batch
		}
		return protocol.Response{Result: json.RawMessage(`{"messages":[]}`)}
	})

	dying := session(t, coord.path, func(dst io.WriteCloser) io.WriteCloser {
		return &brokenWriter{dst: dst, fail: func([]byte) bool {
			mu.Lock()
			defer mu.Unlock()
			return armed
		}}
	})
	if _, err := dying.CallTool(t.Context(), &mcp.CallToolParams{Name: toolInbox}); err == nil {
		t.Fatal("inbox succeeded even though its response could not be written")
	}

	mu.Lock()
	armed = false
	mu.Unlock()

	revived := session(t, coord.path, nil)
	var inbox inboxOutput
	callTool(t, revived, toolInbox, nil, &inbox)
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != "msg_02" {
		t.Fatalf("redelivered inbox = %+v, want the same batch", inbox)
	}
	for _, req := range coord.seen() {
		if req.Method != protocol.MethodCoordInbox {
			continue
		}
		var p protocol.CoordInboxParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatalf("decode inbox request: %v", err)
		}
		if p.AckToken != "" {
			t.Fatalf("a batch that never reached the agent was acknowledged: %s", req.Params)
		}
	}
}

// TestBridgeRedialsAfterListenerRecreation covers the window a bridge lives
// through every server restart: the socket it was provisioned against is
// unlinked and rebound as a new inode. Dialing per tool call means the
// failure is one readable error and the next call simply works.
func TestBridgeRedialsAfterListenerRecreation(t *testing.T) {
	success := golden(t, "success.ndjson")
	coord := newFakeCoord(t, func(protocol.Request) protocol.Response { return success[0] })
	cs := session(t, coord.path, nil)

	var status protocol.CoordStatusResult
	callTool(t, cs, toolStatus, nil, &status)

	coord.stop()
	gone, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: toolStatus})
	if err != nil {
		t.Fatalf("status with no listener: %v", err)
	}
	if code := errorCode(t, gone); code != protocol.CodeUnavailable {
		t.Fatalf("status with no listener: code = %d, want %d", code, protocol.CodeUnavailable)
	}

	coord.listen()
	callTool(t, cs, toolStatus, nil, &status)
	if status.RunID != "run_01" {
		t.Fatalf("status after rebind = %+v", status)
	}
}

// TestGateNeverPromotesABatchTheAgentDidNotSee pins the rule the mailbox's
// one guarantee rests on. Duplicates are harmless; acknowledging a batch
// that reached nobody is silent loss, so every way a token can be staged
// without its own response landing has to leave it unpromoted.
func TestGateNeverPromotesABatchTheAgentDidNotSee(t *testing.T) {
	ctx := t.Context()
	first, second := testID(t, 7), testID(t, 8)

	t.Run("another call's response cannot promote it", func(t *testing.T) {
		g := newGate()
		if err := g.claim(ctx, first); err != nil {
			t.Fatalf("claim: %v", err)
		}
		g.stage(first, "token-a")
		g.release(first)

		if err := g.claim(ctx, second); err != nil {
			t.Fatalf("claim: %v", err)
		}
		g.finish(second, true)
		if g.token() != "" {
			t.Fatalf("an unrelated response promoted the token: %q", g.token())
		}
		g.release(second)

		g.finish(first, true)
		if g.token() != "token-a" {
			t.Fatalf("token = %q, want token-a once its own response landed", g.token())
		}
	})

	// A handler that answers late - after its own call let the slot go -
	// must not have its token credited to whoever holds the slot now.
	t.Run("a late stage cannot land in another call's slot", func(t *testing.T) {
		g := newGate()
		if err := g.claim(ctx, first); err != nil {
			t.Fatalf("claim: %v", err)
		}
		g.release(first)

		if err := g.claim(ctx, second); err != nil {
			t.Fatalf("claim: %v", err)
		}
		g.stage(first, "token-a")
		g.finish(second, true)
		if g.token() != "" {
			t.Fatalf("a late stage was promoted by another call: %q", g.token())
		}
		g.release(second)
	})

	// The exact sequence a cancelled call opens: the client gives up, the
	// handler's socket round trip answers anyway, and a later call's
	// response is written successfully. Neither call may commit that token.
	t.Run("a cancelled call cannot promote it", func(t *testing.T) {
		g := newGate()
		if err := g.claim(ctx, first); err != nil {
			t.Fatalf("claim: %v", err)
		}
		g.cancel(first)
		g.stage(first, "token-a")
		g.release(first)

		if err := g.claim(ctx, second); err != nil {
			t.Fatalf("claim: %v", err)
		}
		g.finish(second, true)
		g.release(second)
		g.finish(first, true)
		if g.token() != "" {
			t.Fatalf("a cancelled call's token was promoted: %q", g.token())
		}
	})

	t.Run("nine pipelined cancellations keep the oldest in flight tombstone", func(t *testing.T) {
		g := newGate()
		if err := g.claim(ctx, first); err != nil {
			t.Fatalf("claim: %v", err)
		}
		g.cancel(first)
		for id := 2; id <= 10; id++ {
			g.cancel(testID(t, float64(id)))
		}
		g.stage(first, "token-a")
		if len(g.pending) != 0 {
			t.Fatalf("cancelled in-flight call staged after pipelined cancellations: %+v", g.pending)
		}
		g.release(first)
	})

	t.Run("a failed write cannot promote it", func(t *testing.T) {
		g := newGate()
		if err := g.claim(ctx, first); err != nil {
			t.Fatalf("claim: %v", err)
		}
		g.stage(first, "token-a")
		g.release(first)
		g.finish(first, false)
		if g.token() != "" {
			t.Fatalf("a response that never landed promoted the token: %q", g.token())
		}
	})

	t.Run("a call that gives up waiting takes no slot", func(t *testing.T) {
		g := newGate()
		if err := g.claim(ctx, first); err != nil {
			t.Fatalf("claim: %v", err)
		}
		abandoned, cancel := context.WithCancel(ctx)
		cancel()
		if err := g.claim(abandoned, second); err == nil {
			t.Fatal("a second inbox call claimed the slot while one was in flight")
		}
		g.release(first)
		if err := g.claim(ctx, second); err != nil {
			t.Fatalf("claim after release: %v", err)
		}
		g.release(second)
	})
}

// TestGateSeesThroughBatchFrames pins the pre-2025-06-18 framing the
// transport still accepts: an inbox call pipelined inside a JSON-RPC batch
// array must be tagged with its own id, and the transport's array-framed
// answer must promote the staged token - or a batching client would be
// redelivered the same inbox batch forever.
func TestGateSeesThroughBatchFrames(t *testing.T) {
	g := newGate()
	line := []byte(`[{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"aether_inbox","arguments":{}}},{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"aether_status","arguments":{}}}]` + "\n")
	batch, ok := decodeBatch(g.incoming(line))
	if !ok || len(batch) != 2 {
		t.Fatalf("incoming did not return a two-message batch: %v", batch)
	}
	req, ok := decodeRequest(batch[0])
	if !ok {
		t.Fatalf("tagged batch element no longer decodes: %s", batch[0])
	}
	var params struct {
		Meta map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Meta[metaCallID] == nil {
		t.Fatalf("inbox call inside a batch was not tagged with its id: %s", batch[0])
	}

	id := testID(t, 7)
	if err := g.claim(t.Context(), id); err != nil {
		t.Fatalf("claim: %v", err)
	}
	g.stage(id, "token-a")
	g.release(id)
	g.outgoing([]byte(`[{"jsonrpc":"2.0","id":7,"result":{}},{"jsonrpc":"2.0","id":8,"result":{}}]`+"\n"), nil)
	if g.token() != "token-a" {
		t.Fatalf("token = %q, want token-a after the batch response landed", g.token())
	}
}

// brokenWriter fails writes once fail reports true, which is how a bridge
// dies between the socket answering and the agent seeing the answer.
type brokenWriter struct {
	dst  io.WriteCloser
	fail func(payload []byte) bool
}

func (w *brokenWriter) Write(p []byte) (int, error) {
	if w.fail(p) {
		return 0, errors.New("stdout is gone")
	}
	return w.dst.Write(p)
}

func (w *brokenWriter) Close() error { return w.dst.Close() }

func testID(t *testing.T, n float64) jsonrpc.ID {
	t.Helper()
	id, err := jsonrpc.MakeID(n)
	if err != nil {
		t.Fatalf("make id: %v", err)
	}
	return id
}

// errorCode reads the Aether error code off a failed tool result. A
// coordination failure is a tool error the agent can read and act on, never
// a protocol error - the MCP transport reserves those codes for its own
// connection states.
func errorCode(t *testing.T, res *mcp.CallToolResult) int {
	t.Helper()
	if !res.IsError {
		t.Fatalf("result is not an error: %+v", res)
	}
	raw, ok := res.Meta[MetaErrorCode]
	if !ok {
		t.Fatalf("result carries no %s: %+v", MetaErrorCode, res.Meta)
	}
	code, ok := raw.(float64)
	if !ok {
		t.Fatalf("%s = %v (%T), want a number", MetaErrorCode, raw, raw)
	}
	return int(code)
}
