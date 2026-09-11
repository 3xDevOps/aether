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

const reportUsage = `report: usage: aether-server report claude   (hook JSON on stdin)
                     aether-server report codex <notify JSON>
                     aether-server report pi --event <name> [--tool <name>]
                     aether-server report opencode --event <name> [--status <type>]`

// report tells the server what the agent is doing. Like mcp it is absent
// from the usage text: no operator runs it. The server stages this binary
// into a run container and the harness's own status callback calls it
// there, against the coordination socket mounted beside it. Each harness
// hands that callback a different shape, which is why the subcommand takes
// four (docs/mcp-bridge.md).
//
// It never fails and never prints to stdout: a callback that breaks or
// slows the agent is worse than a run card that is briefly wrong, so every
// problem is one line on stderr - which the pi, omp and opencode
// extensions read and forward to one warning of their own, and every other
// harness shows only when the callback exits non-zero.
//
// Adding a harness is one case below plus one mapping function in
// internal/agentstatus.
func report(args []string) {
	if len(args) == 0 {
		warn(reportUsage)
		return
	}
	harness, args := args[0], args[1:]
	fs := flag.NewFlagSet("report "+harness, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	socket := fs.String("socket", mcpbridge.SocketPath, "coordination socket to report on")
	event := fs.String("event", "", "the event being reported (opencode, pi, omp)")
	status := fs.String("status", "", "the session status type the event carries (opencode session.status)")
	tool := fs.String("tool", "", "the tool the event names, where it names one (pi, omp)")
	if err := fs.Parse(args); err != nil {
		return
	}
	// Whatever the harness put behind its flags. Only Codex has one: its
	// notify payload. Every other shape is named below with none, so a
	// stray argument is a malformed invocation rather than a silent report.
	payload := fs.Args()
	ctx, cancel := context.WithTimeout(context.Background(), reportBudget)
	defer cancel()
	var (
		rep    agentstatus.Report
		mapped bool
	)
	switch {
	case harness == "claude" && len(payload) == 0:
		hook, err := readHookPayload(ctx, os.Stdin)
		if err != nil {
			warn("report %s: read the hook payload: %v", harness, err)
			return
		}
		rep, mapped = agentstatus.FromClaudeHook(hook)
	case harness == "codex" && len(payload) == 1:
		rep, mapped = agentstatus.FromCodexNotify(payload[0])
	// One reporter name for both CLIs: they load the same extension, which
	// names itself pi whichever of the two is running it.
	case harness == "pi" && len(payload) == 0 && *event != "":
		rep, mapped = agentstatus.FromPiEvent(*event, *tool)
	case harness == "opencode" && len(payload) == 0 && *event != "":
		rep, mapped = agentstatus.FromOpenCodeEvent(*event, *status)
	default:
		warn(reportUsage)
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
