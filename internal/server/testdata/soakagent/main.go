// Command soakagent is the restart acceptance test's in-container agent. It
// runs as one ACP turn of the scripted agent (acpmock.PromptRunPrefix), starts
// a long shell command, and keeps calling the server through the staged CLI
// and the staged MCP bridge for the whole duration, as a busy agent does.
// Every call, every message and every streamed line is recorded with a
// millisecond timestamp in <dir>/<name>.jsonl for the test to read.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type event struct {
	T    int64  `json:"t"`
	Kind string `json:"kind"`
	OK   bool   `json:"ok"`
	Dur  int64  `json:"dur_ms,omitempty"`
	ID   string `json:"id,omitempty"`
	Err  string `json:"err,omitempty"`
}

type recorder struct {
	mu sync.Mutex
	f  *os.File
}

func (r *recorder) add(kind string, start time.Time, id string, err error) {
	e := event{T: start.UnixMilli(), Kind: kind, OK: err == nil, Dur: time.Since(start).Milliseconds(), ID: id}
	if err != nil {
		e.Err = err.Error()
	}
	line, _ := json.Marshal(e)
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.f.Write(append(line, '\n'))
}

func main() {
	name := flag.String("name", "a", "this agent's name, a or b")
	dir := flag.String("dir", "/workspace/soak", "directory for logs and markers")
	duration := flag.Duration("duration", 100*time.Second, "how long the agent keeps working")
	fixedPeer := flag.String("peer", "", "run to send to; without it the peer is discovered through the conflict radar")
	every := flag.Duration("every", 2*time.Second, "interval of the CLI and bridge status probes")
	burstMode := flag.Bool("burst", false, "send a burst of keyed sends and one terminal input when <dir>/burst appears")
	tickN := flag.Int("ticks", 0, "only stream this many numbered lines, 100 ms apart, recording when each was written")
	long := flag.Int("long", 120, "seconds the long shell command runs")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Println("soak-error:", err)
		os.Exit(1)
	}
	f, err := os.OpenFile(filepath.Join(*dir, *name+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Println("soak-error:", err)
		os.Exit(1)
	}
	rec := &recorder{f: f}
	if *tickN > 0 {
		for i := 1; i <= *tickN; i++ {
			rec.add("tick", time.Now(), fmt.Sprint(i), nil)
			fmt.Printf("tick %d\n", i)
			time.Sleep(100 * time.Millisecond)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration+5*time.Minute)
	defer cancel()

	startLong(*dir, *name, *long, rec)
	bridge := &mcpConn{}
	defer bridge.close()
	peer := *fixedPeer
	if peer == "" {
		peer = waitPeer(ctx, bridge, rec, *dir)
	}
	fmt.Println("peer:" + peer)

	end := time.Now().Add(*duration)
	var wg sync.WaitGroup
	loop := func(every time.Duration, fn func(n int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; time.Now().Before(end); n++ {
				fn(n)
				time.Sleep(every)
			}
		}()
	}
	loop(*every, func(int) { cliStatus(ctx, rec) })
	loop(*every, func(n int) {
		if n == 0 {
			time.Sleep(time.Second)
		}
		bridge.status(ctx, rec)
	})
	// The send limit is five and one every five seconds: bursts use it all.
	sendEvery := 6 * time.Second
	if *burstMode {
		sendEvery = 0
		if *name == "a" {
			cliSend(ctx, rec, peer, "a-msg-0000", true)
		} else {
			bridge.send(ctx, rec, peer, "b-msg-0000")
		}
	}
	regular := func(fn func(n int)) {
		if sendEvery > 0 {
			loop(sendEvery, fn)
		}
	}
	if *name == "a" {
		regular(func(n int) { cliSend(ctx, rec, peer, fmt.Sprintf("a-msg-%04d", n), n%2 == 0) })
		loop(0, func(int) { bridge.recv(ctx, rec) })
	} else {
		regular(func(n int) { bridge.send(ctx, rec, peer, fmt.Sprintf("b-msg-%04d", n)) })
		loop(0, func(int) { cliRecv(ctx, rec) })
	}
	if *burstMode {
		term := startTerminal(ctx, *dir)
		trigger := filepath.Join(*dir, "burst")
		loop(50*time.Millisecond, func(int) {
			if os.Remove(trigger) == nil {
				burst(ctx, rec, bridge, term, *name, peer, int(bursts.Add(1)))
			}
		})
	}
	var ticks atomic.Int64
	loop(100*time.Millisecond, func(int) { fmt.Printf("tick %d\n", ticks.Add(1)) })
	wg.Wait()
	rec.add("end", time.Now(), fmt.Sprint(ticks.Load()), nil)
	fmt.Println("soak-done", ticks.Load())
}

var bursts atomic.Int64

// burst fires two rounds of keyed sends (coord.send allows bursts of five) through the CLI and the bridge, and one
// unkeyed terminal input, while the harness takes the server down.
func burst(ctx context.Context, rec *recorder, b *mcpConn, term *terminal, name, peer string, n int) {
	var wg sync.WaitGroup
	if term != nil {
		wg.Add(1)
		go func() { defer wg.Done(); term.input(ctx, rec, fmt.Sprintf("term-%s-%d", name, n)) }()
	}
	for round := range 2 {
		body := fmt.Sprintf("%s-burst-%d-%d", name, n, round)
		wg.Add(2)
		go func() { defer wg.Done(); cliSend(ctx, rec, peer, body+"-cli", true) }()
		go func() { defer wg.Done(); b.send(ctx, rec, peer, body+"-mcp") }()
		time.Sleep(150 * time.Millisecond)
	}
	wg.Wait()
}

// terminal is a development PTY whose process appends every line it reads to
// <dir>/term.log, so the harness can count how often an input was applied.
type terminal struct {
	id, incarnation, session string
	generation               uint64
	// cursor is where the last output read ended; it is held across restarts.
	cursor *protocol.DevOutputCursor
}

func cliJSON(ctx context.Context, params any, args ...string) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, coordtransport.CLIPath, append(args, "--params-file", "-")...)
	cmd.Stdin = bytes.NewReader(raw)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s%s", err, out.String(), errb.String())
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		return nil, err
	}
	return env.Result, nil
}

func startTerminal(ctx context.Context, dir string) *terminal {
	script := fmt.Sprintf(`while IFS= read -r l; do echo "$l" >> %s; echo "out:$l"; done`, filepath.Join(dir, "term.log"))
	res, err := cliJSON(ctx, protocol.DevTerminalStartParams{Name: "soak-term", Command: []string{"sh", "-c", script}}, "terminal", "start")
	if err != nil {
		fmt.Println("soak-error: terminal start:", err)
		return nil
	}
	var started protocol.DevTerminalStartResult
	if err = json.Unmarshal(res, &started); err != nil {
		return nil
	}
	t := &terminal{id: started.Terminal.TerminalID, incarnation: started.Terminal.Incarnation, session: "soak-control"}
	res, err = cliJSON(ctx, map[string]any{
		"surface":            protocol.DevSurface{Kind: "terminal", ID: t.id, Incarnation: t.incarnation},
		"control_session_id": t.session,
	}, "control", "acquire")
	if err != nil {
		fmt.Println("soak-error: control acquire:", err)
		return nil
	}
	var acquired protocol.DevControlAcquireResult
	if err = json.Unmarshal(res, &acquired); err != nil || acquired.Controller == nil {
		return nil
	}
	t.generation = acquired.Controller.ControlGeneration
	// The first read, without a cursor, starts at the terminal's oldest
	// byte and establishes the cursor every later read continues from.
	if _, err = t.read(ctx, nil); err != nil {
		fmt.Println("soak-error: terminal output:", err)
		return nil
	}
	return t
}

func (t *terminal) input(ctx context.Context, rec *recorder, token string) {
	start := time.Now()
	_, err := cliJSON(ctx, protocol.DevTerminalInputParams{
		DevTerminalTarget: protocol.DevTerminalTarget{TerminalID: t.id, Incarnation: t.incarnation},
		DevControlFence:   protocol.DevControlFence{ControlSessionID: t.session, ControlGeneration: t.generation},
		Kind:              "text", Text: token + "\n",
	}, "terminal", "input")
	rec.add("term-input", start, token, err)
	if err == nil {
		t.output(ctx, rec, token)
	}
}

// read reads the output after cursor up to its current end and moves the
// held cursor there.
func (t *terminal) read(ctx context.Context, cursor *protocol.DevOutputCursor) ([]protocol.DevTerminalOutputResult, error) {
	var pages []protocol.DevTerminalOutputResult
	for range 50 {
		res, err := cliJSON(ctx, protocol.DevTerminalOutputParams{
			DevTerminalTarget: protocol.DevTerminalTarget{TerminalID: t.id, Incarnation: t.incarnation},
			After:             cursor, Format: "text",
		}, "terminal", "output")
		if err != nil {
			return pages, err
		}
		var out protocol.DevTerminalOutputResult
		if err = json.Unmarshal(res, &out); err != nil {
			return pages, err
		}
		pages = append(pages, out)
		next := out.Next
		cursor, t.cursor = &next, &next
		if !out.More {
			break
		}
	}
	return pages, nil
}

// output reads from the cursor held before the last restart and records
// whether the echo of token arrived without a gap. The echo may follow the
// accepted input by a moment, so a read that does not show it yet is
// repeated from where it ended.
func (t *terminal) output(ctx context.Context, rec *recorder, token string) {
	start := time.Now()
	var text strings.Builder
	for attempt := 0; ; attempt++ {
		held := t.cursor
		pages, err := t.read(ctx, held)
		if err != nil {
			rec.add("term-output", start, token, err)
			return
		}
		for _, out := range pages {
			switch {
			case out.MissingCursor:
				rec.add("term-output", start, token, fmt.Errorf("missing_cursor: the cursor %+v is not in the output, which starts at %+v", *held, out.Start))
				return
			case out.Truncated:
				rec.add("term-output", start, token, fmt.Errorf("truncated: output before %+v was evicted", out.Start))
				return
			}
			text.WriteString(out.Text)
		}
		if strings.Contains(text.String(), "out:"+token) {
			rec.add("term-output", start, token, nil)
			return
		}
		if attempt == 20 {
			rec.add("term-output", start, token, errors.New("the echo of the input never appeared in the output"))
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// startLong runs the long command as a plain child, the way an agent's shell
// tool does, appending a line a second to a marker file.
func startLong(dir, name string, seconds int, rec *recorder) {
	marker := filepath.Join(dir, "marker."+name)
	script := fmt.Sprintf(`i=0; while [ $i -lt %d ]; do i=$((i+1)); echo $i >> %s; sleep 1; done; echo ok > %s`,
		seconds, marker, filepath.Join(dir, "long.done."+name))
	cmd := exec.Command("sh", "-c", script)
	start := time.Now()
	err := cmd.Start()
	rec.add("long", start, fmt.Sprint(cmd.Process.Pid), err)
}

func waitPeer(ctx context.Context, b *mcpConn, rec *recorder, dir string) string {
	var touched time.Time
	for {
		if time.Since(touched) > 10*time.Second {
			_ = os.WriteFile(filepath.Join(filepath.Dir(dir), "shared.txt"), []byte(time.Now().String()), 0o644)
			touched = time.Now()
		}
		var st protocol.CoordStatusResult
		if err := b.call(ctx, "aether_status", nil, &st); err == nil && len(st.Peers) > 0 {
			return st.Peers[0].RunID
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func cli(ctx context.Context, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, coordtransport.CLIPath, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%w: %s%s", err, out.String(), errb.String())
	}
	return out.Bytes(), nil
}

func cliStatus(ctx context.Context, rec *recorder) {
	start := time.Now()
	_, err := cli(ctx, "status", "--json")
	rec.add("cli", start, "", err)
}

func cliSend(ctx context.Context, rec *recorder, peer, body string, keyed bool) {
	start := time.Now()
	args := []string{"send", "--to", peer, "--body", body}
	if keyed {
		args = append(args, "--idempotency-key", "soak-"+body)
	}
	_, err := cli(ctx, args...)
	rec.add("send", start, body, err)
}

func cliRecv(ctx context.Context, rec *recorder) {
	start := time.Now()
	out, err := cli(ctx, "inbox", "--wait", "20")
	var env struct {
		Result protocol.CoordInboxResult `json:"result"`
	}
	if err == nil {
		err = json.Unmarshal(out, &env)
	}
	if err != nil {
		rec.add("recv-call", start, "", err)
		time.Sleep(100 * time.Millisecond)
		return
	}
	rec.add("recv-call", start, "", nil)
	for _, m := range env.Result.Messages {
		rec.add("recv", time.Now(), m.Body, nil)
	}
}

// mcpConn is a client of the staged MCP bridge that reconnects, as an agent
// harness restarts a dead MCP server.
type mcpConn struct {
	mu    sync.Mutex
	cs    *mcp.ClientSession
	ack   string
	rcvMu sync.Mutex
}

func (m *mcpConn) session(ctx context.Context) (*mcp.ClientSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cs != nil {
		return m.cs, nil
	}
	cmd := exec.Command(coordtransport.BinaryPath, "mcp")
	client := mcp.NewClient(&mcp.Implementation{Name: "soakagent", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to the bridge: %w", err)
	}
	m.cs = cs
	return cs, nil
}

func (m *mcpConn) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cs != nil {
		_ = m.cs.Close()
		m.cs = nil
	}
}

func (m *mcpConn) call(ctx context.Context, tool string, args, out any) error {
	cs, err := m.session(ctx)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	res, err := cs.CallTool(cctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		m.close()
		return fmt.Errorf("%s: %w", tool, err)
	}
	if res.IsError {
		var text []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text = append(text, tc.Text)
			}
		}
		return fmt.Errorf("%s: tool error: %s", tool, strings.Join(text, " "))
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (m *mcpConn) status(ctx context.Context, rec *recorder) {
	start := time.Now()
	err := m.call(ctx, "aether_status", nil, nil)
	rec.add("mcp", start, "", err)
}

func (m *mcpConn) send(ctx context.Context, rec *recorder, peer, body string) {
	start := time.Now()
	args := protocol.CoordSendParams{ToRunID: peer, Body: body, IdempotencyKey: "soak-" + body}
	err := m.call(ctx, "aether_send", args, nil)
	rec.add("send", start, body, err)
}

func (m *mcpConn) recv(ctx context.Context, rec *recorder) {
	m.rcvMu.Lock()
	defer m.rcvMu.Unlock()
	start := time.Now()
	var inbox protocol.CoordInboxResult
	err := m.call(ctx, "aether_inbox", protocol.CoordInboxParams{AckToken: m.ack, WaitSeconds: 20}, &inbox)
	if err != nil {
		rec.add("recv-call", start, "", err)
		time.Sleep(100 * time.Millisecond)
		return
	}
	rec.add("recv-call", start, "", nil)
	m.ack = inbox.AckToken
	for _, msg := range inbox.Messages {
		rec.add("recv", time.Now(), msg.Body, nil)
	}
}
