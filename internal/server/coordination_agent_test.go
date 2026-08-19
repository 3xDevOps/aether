//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// The coordination E2E's fake agent. It plays the part a real harness
// plays: it edits a file the radar can see it shares with a peer, it reads
// what lands in its terminal, and - when its launch profile registered the
// MCP bridge - it settles the overlap through the three tools.
//
// The bridge it drives is the real one, speaking the real coordination
// wire on the socket its own mount carries; it runs in process rather than
// as the staged binary because under `go test` /proc/self/exe is the test
// binary, which has no mcp subcommand.
//
// The agent never touches *testing.T: it runs on the container's goroutine
// and outlives some assertions, so everything it has to say it says on its
// terminal, where the test reads it over a real SSH attach.

// The three tools, as an agent sees them.
const (
	toolStatus = "aether_status"
	toolSend   = "aether_send"
	toolInbox  = "aether_inbox"
)

// coordShared is the file every coordination run edits, which is what puts
// them in the radar's conflict set.
const coordShared = "shared.txt"

// coordPoll bounds an agent's wait on the radar and on its inbox.
const coordPoll = 2 * time.Minute

type coordAgent struct {
	// peer is the task of the run this agent messages. Empty means it only
	// reports the surfaces it was given: the notice-only degradation.
	peer string
	// body is the message it sends.
	body string
	// release keeps the agent, and so its run, alive until the test is done
	// with it.
	release <-chan struct{}
}

func (a coordAgent) run(ctx context.Context, c *e2eContainer) {
	// The notice arrives on the agent's stdin and the write carrying it
	// blocks until it is read, so drain stdin for the whole life of the
	// run: an agent that reads late stalls the injector for every peer.
	go func() {
		for {
			line, ok := c.readStdinLine()
			if !ok {
				return
			}
			c.output("notice:" + line + "\r\n")
		}
	}()
	// The diff watch is registered just after the container starts, and a
	// write it never saw is a run the radar never hears about.
	time.Sleep(time.Second)
	shared := filepath.Join(c.spec.WorktreeHostPath, coordShared)
	if err := os.WriteFile(shared, []byte("edited by "+c.spec.Env["AETHER_RUN_ID"]+"\n"), 0o644); err != nil {
		c.output("agent-error: write " + shared + ": " + err.Error() + "\r\n")
		return
	}
	a.coordinate(ctx, c)
	select {
	case <-a.release:
	case <-ctx.Done():
	case <-c.done:
	}
}

// coordinate reports which coordination surfaces the run actually got and,
// when the harness was registered with the bridge, uses them: find the
// peer through aether_status, message it, and wait for its reply.
func (a coordAgent) coordinate(ctx context.Context, c *e2eContainer) {
	dir, mounted := c.mount(mcpbridge.MountDir)
	if !mounted {
		c.output("assets:none\r\n")
		return
	}
	_, err := os.Stat(filepath.Join(dir.HostPath, coord.ConfigName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		c.output("assets:notice-only\r\n")
		return
	case err != nil:
		c.output("agent-error: stat mcp config: " + err.Error() + "\r\n")
		return
	}
	c.output("assets:mcp\r\n")
	if a.peer == "" {
		return
	}

	cs, closeBridge, err := bridgeSession(ctx, filepath.Join(dir.HostPath, coord.SocketName))
	if err != nil {
		c.output("agent-error: " + err.Error() + "\r\n")
		return
	}
	defer closeBridge()

	peer, err := waitPeer(ctx, cs, a.peer)
	if err != nil {
		c.output("agent-error: " + err.Error() + "\r\n")
		return
	}
	var sent protocol.CoordSendResult
	if _, serr := callTool(ctx, cs, toolSend, protocol.CoordSendParams{ToRunID: peer.RunID, Body: a.body}, &sent); serr != nil {
		c.output("agent-error: " + serr.Error() + "\r\n")
		return
	}
	c.output("sent:" + sent.MessageID + "\r\n")
	msg, err := waitInbox(ctx, cs)
	if err != nil {
		c.output("agent-error: " + err.Error() + "\r\n")
		return
	}
	c.output("inbox:" + msg.Body + "\r\n")
}

// waitPeer polls aether_status until the radar authorizes the run to
// message the peer running task.
func waitPeer(ctx context.Context, cs *mcp.ClientSession, task string) (protocol.CoordPeer, error) {
	deadline := time.Now().Add(coordPoll)
	for {
		var status protocol.CoordStatusResult
		if _, err := callTool(ctx, cs, toolStatus, nil, &status); err != nil {
			return protocol.CoordPeer{}, err
		}
		for _, p := range status.Peers {
			if p.Task == task {
				return p, nil
			}
		}
		if err := coordSleep(ctx, deadline); err != nil {
			return protocol.CoordPeer{}, fmt.Errorf("no peer running %q: %w", task, err)
		}
	}
}

// waitInbox polls aether_inbox until a peer's message is delivered.
func waitInbox(ctx context.Context, cs *mcp.ClientSession) (protocol.CoordMessage, error) {
	deadline := time.Now().Add(coordPoll)
	for {
		var inbox struct {
			Messages []protocol.CoordMessage `json:"messages"`
		}
		if _, err := callTool(ctx, cs, toolInbox, nil, &inbox); err != nil {
			return protocol.CoordMessage{}, err
		}
		if len(inbox.Messages) > 0 {
			return inbox.Messages[0], nil
		}
		if err := coordSleep(ctx, deadline); err != nil {
			return protocol.CoordMessage{}, fmt.Errorf("empty inbox: %w", err)
		}
	}
}

// coordSleep waits out one polling interval, reporting why it stopped when
// the deadline or the context ran out first.
func coordSleep(ctx context.Context, deadline time.Time) error {
	if time.Now().After(deadline) {
		return fmt.Errorf("gave up after %s", coordPoll)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(200 * time.Millisecond):
		return nil
	}
}

// bridgeSession runs the real MCP bridge against sock and returns a
// connected MCP client session - the pairing a registered harness gets
// inside its container.
func bridgeSession(ctx context.Context, sock string) (*mcp.ClientSession, func(), error) {
	toBridge, fromClient := io.Pipe()
	toClient, fromBridge := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mcpbridge.Run(ctx, mcpbridge.Config{Socket: sock, In: toBridge, Out: fromBridge})
	}()
	stop := func() {
		_ = fromClient.Close()
		_ = toClient.Close()
		<-done
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-agent", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, &mcp.IOTransport{Reader: toClient, Writer: fromClient}, nil)
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("connect to the MCP bridge: %w", err)
	}
	return cs, func() {
		_ = cs.Close()
		stop()
	}, nil
}

// callTool makes one MCP tool call, decoding its structured result into
// out. A tool error is returned as an error with the result kept, so a
// caller can read the Aether code the bridge reported under it.
func callTool(ctx context.Context, cs *mcp.ClientSession, name string, args, out any) (*mcp.CallToolResult, error) {
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError {
		return res, fmt.Errorf("%s reported a tool error: %+v", name, res.Content)
	}
	if out != nil {
		raw, merr := json.Marshal(res.StructuredContent)
		if merr != nil {
			return res, fmt.Errorf("%s: re-marshal result: %w", name, merr)
		}
		if uerr := json.Unmarshal(raw, out); uerr != nil {
			return res, fmt.Errorf("%s: decode result: %w", name, uerr)
		}
	}
	return res, nil
}

// toolErrorCode is the Aether error code the bridge reported for a failed
// tool call, or zero when it carried none.
func toolErrorCode(res *mcp.CallToolResult) int {
	if res == nil {
		return 0
	}
	code, ok := res.Meta[mcpbridge.MetaErrorCode].(float64)
	if !ok {
		return 0
	}
	return int(code)
}
