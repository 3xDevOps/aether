package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// reportBudget is the whole time the reporter may spend, from the first
// byte of the hook payload through the call. It sits under the timeout the
// harness gives the hook, because the hook runs on the agent's own turn
// boundaries: being late is worse than being wrong.
const reportBudget = 4 * time.Second

// maxHookPayload bounds the hook body read from stdin. A payload is not
// only metadata: Claude Code's tool events carry the tool's own input and
// response verbatim, so writing a large file produces a payload the size of
// that file. The cap keeps a runaway one from being read into memory on the
// agent's own turn boundary, and is far above any real tool call.
const maxHookPayload = 8 << 20

// report tells the server what the agent is doing. Like mcp it is absent
// from the usage text: no operator runs it. The server stages this binary
// into a run container and the harness's own status hooks call it there,
// with the hook payload on stdin, against the coordination socket mounted
// beside it:
//
//	aether-server report claude
//
// It never fails and never prints to stdout: a hook that breaks or slows
// the agent is worse than a run card that is briefly wrong, so every
// problem is one line on stderr - which the harness shows only when the
// hook exits non-zero, so in normal use it is invisible and greppable in
// the transcript.
//
// Adding a harness is one case below plus one mapping function in
// internal/agentstatus.
func report(args []string) {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	socket := fs.String("socket", mcpbridge.SocketPath, "coordination socket to report on")
	if err := fs.Parse(args); err != nil {
		return
	}
	if fs.NArg() != 1 {
		warn("report: usage: aether-server report <harness>")
		return
	}
	harness := fs.Arg(0)
	ctx, cancel := context.WithTimeout(context.Background(), reportBudget)
	defer cancel()
	var (
		rep    agentstatus.Report
		mapped bool
	)
	switch harness {
	case "claude":
		payload, err := readHookPayload(ctx, os.Stdin)
		if err != nil {
			warn("report %s: read the hook payload: %v", harness, err)
			return
		}
		rep, mapped = agentstatus.FromClaudeHook(payload)
	default:
		warn("report: unknown harness %q", harness)
		return
	}
	// An event that says nothing about whether the agent is working or
	// waiting is not worth a round trip.
	if !mapped {
		return
	}
	err := mcpbridge.Call(ctx, *socket, protocol.MethodRunReport, protocol.RunReportParams{
		State:  string(rep.State),
		Reason: rep.Reason,
	}, nil)
	if err != nil {
		warn("report %s: %v", harness, err)
	}
}

// readHookPayload reads the hook body under the same budget as the call.
// The harness owns the write end of this pipe and the reporter is the last
// thing between the agent and its next turn, so a harness that hands over a
// pipe and forgets to close it must not leave the reporter waiting on it.
// The read goroutine outlives the wait and the process exits behind it.
//
// It reads one byte past the cap so an oversized payload is an error the
// caller can name. Truncating it instead would hand the mapping half a
// JSON document, which is indistinguishable from an event Aether ignores
// and would drop the report without a word.
func readHookPayload(ctx context.Context, r io.Reader) ([]byte, error) {
	type read struct {
		payload []byte
		err     error
	}
	done := make(chan read, 1)
	go func() {
		payload, err := io.ReadAll(io.LimitReader(r, maxHookPayload+1))
		done <- read{payload, err}
	}()
	select {
	case res := <-done:
		if res.err == nil && len(res.payload) > maxHookPayload {
			return nil, fmt.Errorf("the payload is larger than %d bytes", maxHookPayload)
		}
		return res.payload, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func warn(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "aether-server "+format+"\n", args...)
}
