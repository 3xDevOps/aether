package mcpbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// gate keeps the inbox acknowledgement token below MCP.
//
// A batch leaves the run's unread set only when the *next* inbox read
// presents the token that names it, so a token may be promoted only once
// the batch it names has actually reached the agent. "Reached the agent" is
// a write on the MCP stream, which happens after the tool handler has
// already returned - so the handler stages the token and the transport
// promotes it.
//
// Everything is keyed by the JSON-RPC id of the call. That is the whole
// safety property: inbox handlers really do run concurrently (the SDK
// dispatches every non-initialize call asynchronously) and a call the
// client cancels keeps running to its own timeout, so a staged token that
// was not keyed to its own call could be promoted by a *different* call's
// response and acknowledge a batch no agent ever saw. Duplicates are
// harmless; that would be silent loss, which the mailbox forbids.
//
// The id is not visible to a tool handler, so the reader stamps it into the
// call's `_meta` on the way past and the handler reads it back out.
//
// The slot serializes inbox calls to one socket round trip at a time. It is
// claimed by the handler, not by the reader: the reader is the transport's
// single decoder goroutine, and blocking there would stall the whole
// session - including the cancellation that would unblock it.
//
// Anything that is not a completed write leaves the token unpromoted: a
// failed write, an error response, a cancelled call, a killed bridge. The
// batch is then still unacknowledged and the next read delivers it again.
type gate struct {
	slot chan struct{}

	mu        sync.Mutex
	committed string
	// pending holds each in-flight call's staged token until its response
	// is written, keyed and ordered by call id.
	pending map[jsonrpc.ID]string
	order   []jsonrpc.ID
	// cancelled marks calls the client has abandoned. Their response may
	// still be written - a cancelled context does not un-read a batch the
	// socket already handed over - and the client discards it, so their
	// token must never be promoted.
	cancelled      map[jsonrpc.ID]bool
	cancelledOrder []jsonrpc.ID
	active         jsonrpc.ID
	held           bool
}

// maxPending bounds the staged tokens held for responses that have not been
// written yet. Only a client that pipelines inbox calls and then abandons
// them can approach it, and dropping the oldest costs exactly one
// redelivered batch - the safe direction.
const maxPending = 8

func newGate() *gate {
	return &gate{
		slot:      make(chan struct{}, 1),
		pending:   make(map[jsonrpc.ID]string),
		cancelled: make(map[jsonrpc.ID]bool),
	}
}

// token is the acknowledgement the next inbox read carries: the token of
// the last batch that reached the agent, or empty when there is none.
func (g *gate) token() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.committed
}

// claim takes the inbox slot for this call, waiting for any call already in
// flight. It gives up when ctx is cancelled so a client that walked away
// cannot pin the slot for the rest of the session.
func (g *gate) claim(ctx context.Context, id jsonrpc.ID) error {
	select {
	case g.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	g.active, g.held = id, true
	g.mu.Unlock()
	return nil
}

// release hands the slot back once this call's socket round trip is done.
// Its staged token stays behind, waiting for the response to be written.
func (g *gate) release(id jsonrpc.ID) {
	g.mu.Lock()
	if g.held && g.active == id {
		g.active, g.held = jsonrpc.ID{}, false
	}
	g.mu.Unlock()
	<-g.slot
}

// stage records the token of the batch this call is about to answer with.
// A stage from a call that no longer holds the slot - a cancelled handler
// returning late - is dropped rather than credited to whoever holds it now.
func (g *gate) stage(id jsonrpc.ID, token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// An unidentifiable or abandoned call stages nothing: with no id to
	// promote it against, or no client left to read it, the only safe
	// reading is that its batch never landed, so it redelivers instead of
	// being acknowledged on a guess.
	if !id.IsValid() || g.cancelled[id] || !g.held || g.active != id {
		return
	}
	if _, ok := g.pending[id]; !ok {
		g.order = append(g.order, id)
	}
	g.pending[id] = token
	for len(g.order) > maxPending {
		delete(g.pending, g.order[0])
		g.order = g.order[1:]
	}
}

// finish retires the call's staged token, promoting it only when the
// response carrying its batch actually reached the agent. A call that
// staged nothing, or whose id is not one of ours, changes nothing.
func (g *gate) finish(id jsonrpc.ID, delivered bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if token, ok := g.pending[id]; ok {
		if delivered {
			g.committed = token
		}
		delete(g.pending, id)
		g.order = withoutID(g.order, id)
	}
	if g.cancelled[id] {
		delete(g.cancelled, id)
		g.cancelledOrder = withoutID(g.cancelledOrder, id)
	}
}

// cancel marks a call abandoned and drops anything it staged. It is
// deliberately not a slot release: the handler still holds the slot until
// its own round trip returns, so a late socket answer cannot land in the
// next call's place.
func (g *gate) cancel(id jsonrpc.ID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.pending[id]; ok {
		delete(g.pending, id)
		g.order = withoutID(g.order, id)
	}
	if !g.cancelled[id] {
		g.cancelled[id] = true
		g.cancelledOrder = append(g.cancelledOrder, id)
	}
	for len(g.cancelledOrder) > maxPending {
		delete(g.cancelled, g.cancelledOrder[0])
		g.cancelledOrder = g.cancelledOrder[1:]
	}
}

func withoutID(ids []jsonrpc.ID, id jsonrpc.ID) []jsonrpc.ID {
	for i, candidate := range ids {
		if candidate == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// reader wraps the MCP input stream so each inbox call carries its own
// JSON-RPC id into the handler that serves it.
func (g *gate) reader(in io.ReadCloser) io.ReadCloser {
	return &lineReader{src: in, br: bufio.NewReader(in), inspect: g.incoming}
}

// writer wraps the MCP output stream. The transport writes one message per
// call, so a completed Write is the moment a response reached the agent.
func (g *gate) writer(out io.WriteCloser) io.WriteCloser {
	return &lineWriter{dst: out, inspect: g.outgoing}
}

// incoming returns the bytes to hand the transport's decoder, which for an
// inbox call is the same message with its id stamped into `_meta`. A
// JSON-RPC 2.0 batch frame - pre-2025-06-18 clients may still send one, and
// the transport accepts it - is inspected message by message, so an inbox
// call pipelined inside a batch is tagged like any other.
func (g *gate) incoming(line []byte) []byte {
	if batch, ok := decodeBatch(line); ok {
		changed := false
		for i, raw := range batch {
			if rewritten, ok := g.inspectIncoming(raw); ok {
				batch[i], changed = rewritten, true
			}
		}
		if !changed {
			return line
		}
		encoded, err := json.Marshal(batch)
		if err != nil {
			return line
		}
		return append(encoded, '\n')
	}
	if rewritten, ok := g.inspectIncoming(line); ok {
		return append(rewritten, '\n')
	}
	return line
}

// inspectIncoming handles one decoded message: it records cancellations and
// reports a rewritten inbox tools/call carrying its own id, or ok=false for
// a message that passes through untouched.
func (g *gate) inspectIncoming(raw []byte) (rewritten []byte, ok bool) {
	req, ok := decodeRequest(raw)
	if !ok {
		return nil, false
	}
	switch req.Method {
	case methodToolsCall:
		if !req.ID.IsValid() || toolCallName(req.Params) != toolInbox {
			return nil, false
		}
		return tagCallID(req)
	case methodCancelled:
		// The client has stopped waiting. Whatever that call ends up
		// putting on the wire, the agent will not read it, so its batch
		// must stay unacknowledged.
		var cancelled struct {
			RequestID any `json:"requestId"`
		}
		if err := json.Unmarshal(req.Params, &cancelled); err != nil {
			return nil, false
		}
		if id, err := jsonrpc.MakeID(cancelled.RequestID); err == nil {
			g.cancel(id)
		}
	}
	return nil, false
}

// outgoing sees every write the transport makes. The transport answers a
// batch with a single array-framed write, so that frame too is unpacked, or
// a batching client's staged tokens would never be promoted and the same
// inbox batch would be redelivered forever.
func (g *gate) outgoing(line []byte, err error) {
	if batch, ok := decodeBatch(line); ok {
		for _, raw := range batch {
			g.finishResponse(raw, err)
		}
		return
	}
	g.finishResponse(line, err)
}

func (g *gate) finishResponse(raw []byte, err error) {
	msg, derr := jsonrpc.DecodeMessage(raw)
	if derr != nil {
		return
	}
	resp, ok := msg.(*jsonrpc.Response)
	if !ok {
		return
	}
	g.finish(resp.ID, err == nil && resp.Error == nil)
}

// decodeBatch splits a JSON-RPC 2.0 batch frame into its member messages.
// Anything that is not a JSON array reports false.
func decodeBatch(line []byte) ([]json.RawMessage, bool) {
	trimmed := bytes.TrimLeft(line, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var batch []json.RawMessage
	if err := json.Unmarshal(trimmed, &batch); err != nil {
		return nil, false
	}
	return batch, true
}

func decodeRequest(line []byte) (*jsonrpc.Request, bool) {
	msg, err := jsonrpc.DecodeMessage(line)
	if err != nil {
		return nil, false
	}
	req, ok := msg.(*jsonrpc.Request)
	return req, ok
}

func toolCallName(params json.RawMessage) string {
	var call struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return ""
	}
	return call.Name
}

// tagCallID rewrites one tools/call so its params carry the call's
// JSON-RPC id under metaCallID. MCP gives a tool handler its arguments and
// its `_meta`, never the id of the request underneath, and the handler
// needs the id to stage a token against its own call. Existing `_meta`
// entries are preserved; a message that cannot be rewritten passes through
// untouched and its handler simply stages nothing.
func tagCallID(req *jsonrpc.Request) ([]byte, bool) {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, false
	}
	meta := map[string]json.RawMessage{}
	if raw, ok := params["_meta"]; ok {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, false
		}
	}
	id, err := json.Marshal(req.ID.Raw())
	if err != nil {
		return nil, false
	}
	meta[metaCallID] = id
	encodedMeta, err := json.Marshal(meta)
	if err != nil {
		return nil, false
	}
	params["_meta"] = encodedMeta
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return nil, false
	}
	req.Params = encodedParams
	tagged, err := jsonrpc.EncodeMessage(req)
	if err != nil {
		return nil, false
	}
	return tagged, true
}

// MCP methods the gate has to recognize on the raw stream.
const (
	methodToolsCall = "tools/call"
	methodCancelled = "notifications/cancelled"
)

// lineReader hands the transport's decoder one newline-delimited message at
// a time, as inspect returns it.
type lineReader struct {
	src     io.Closer
	br      *bufio.Reader
	inspect func([]byte) []byte
	buf     []byte
	err     error
}

func (r *lineReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		line, err := r.br.ReadBytes('\n')
		r.err = err
		if len(line) == 0 {
			continue
		}
		r.buf = r.inspect(line)
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *lineReader) Close() error { return r.src.Close() }

// lineWriter reports every message the transport writes, and whether the
// write succeeded.
type lineWriter struct {
	dst     io.WriteCloser
	inspect func([]byte, error)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	w.inspect(p, err)
	return n, err
}

func (w *lineWriter) Close() error { return w.dst.Close() }
