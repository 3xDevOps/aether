package acpmock

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCancelRightAfterPromptEndsTheWaitTurn(t *testing.T) {
	fix, err := Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	go New(fix).Serve(agentIn, agentOut)
	t.Cleanup(func() { _ = hostOut.Close(); _ = hostIn.Close() })
	replies := bufio.NewScanner(hostIn)
	for id := 1; id <= 200; id++ {
		turn := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"session/prompt","params":{"sessionId":"s","prompt":[{"type":"text","text":%q}]}}`+"\n"+
			`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`+"\n", id, PromptWait)
		go func() { _, _ = io.WriteString(hostOut, turn) }()
		got := make(chan string, 1)
		go func() {
			replies.Scan()
			got <- replies.Text()
		}()
		select {
		case reply := <-got:
			if !strings.Contains(reply, `"stopReason":"cancelled"`) {
				t.Fatalf("turn %d: reply %s, want stopReason cancelled", id, reply)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("turn %d: the cancel sent right after the prompt did not end the turn", id)
		}
	}
}

// runPrompt opens a session with meta, sends prompt, and returns the agent's
// lines through the end of the turn plus every line that followed within
// wait.
func runPrompt(t *testing.T, meta, prompt string, wait time.Duration) (inTurn, after []string) {
	t.Helper()
	fix, err := Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	go New(fix).Serve(agentIn, agentOut)
	t.Cleanup(func() { _ = hostOut.Close(); _ = hostIn.Close() })
	lines := make(chan string, 64)
	go func() {
		scan := bufio.NewScanner(hostIn)
		for scan.Scan() {
			lines <- scan.Text()
		}
		close(lines)
	}()
	send := func(line string) { go func() { _, _ = io.WriteString(hostOut, line+"\n") }() }
	send(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/","mcpServers":[]%s}}`, meta))
	nextLine(t, lines)
	send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":%q,"prompt":[{"type":"text","text":%q}]}}`, fix.SessionID(), prompt))
	for {
		line := nextLine(t, lines)
		inTurn = append(inTurn, line)
		if strings.Contains(line, `"stopReason"`) {
			break
		}
	}
	timeout := time.After(wait)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return inTurn, after
			}
			after = append(after, line)
		case <-timeout:
			return inTurn, after
		}
	}
}

const askForFrames = `,"_meta":{"claudeCode":{"emitRawSDKMessages":[{"type":"system","subtype":"background_tasks_changed"}]}}`

func TestPromptBackgroundRunsAFollowUpAfterTheTurn(t *testing.T) {
	delay := BackgroundDelay
	BackgroundDelay = 50 * time.Millisecond
	t.Cleanup(func() { BackgroundDelay = delay })
	inTurn, after := runPrompt(t, askForFrames, PromptBackground, 400*time.Millisecond)
	turn, followUp := strings.Join(inTurn, "\n"), strings.Join(after, "\n")
	if !strings.Contains(turn, `"_claude/sdkMessage"`) || !strings.Contains(turn, `"task_id":"`+BackgroundTaskID+`"`) || !strings.Contains(inTurn[len(inTurn)-1], `"stopReason":"end_turn"`) {
		t.Fatalf("turn output:\n%s", turn)
	}
	for _, want := range []string{`"subtype":"task_notification"`, `"subtype":"task_updated"`, "background done", `"_claude/origin":{"kind":"task-notification"}`} {
		if !strings.Contains(followUp, want) {
			t.Fatalf("follow-up lacks %s:\n%s", want, followUp)
		}
	}
}

func TestPromptBackgroundSendsNoFramesUnlessAsked(t *testing.T) {
	delay := BackgroundDelay
	BackgroundDelay = 50 * time.Millisecond
	t.Cleanup(func() { BackgroundDelay = delay })
	inTurn, after := runPrompt(t, "", PromptBackground, 400*time.Millisecond)
	all := strings.Join(append(inTurn, after...), "\n")
	if strings.Contains(all, "_claude/sdkMessage") || !strings.Contains(all, "background done") {
		t.Fatalf("output:\n%s", all)
	}
}

func TestPromptDevServerNeverEnds(t *testing.T) {
	inTurn, after := runPrompt(t, askForFrames, PromptDevServer, 300*time.Millisecond)
	turn := strings.Join(inTurn, "\n")
	if !strings.Contains(turn, `"task_type":"local_bash"`) || len(after) != 0 {
		t.Fatalf("turn output:\n%s\nafter it: %v", turn, after)
	}
}

func TestPromptHoldKeepsTheWorkflowLiveUntilRelease(t *testing.T) {
	delay := BackgroundDelay
	BackgroundDelay = 50 * time.Millisecond
	t.Cleanup(func() { BackgroundDelay = delay })
	fix, err := Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	go New(fix).Serve(agentIn, agentOut)
	t.Cleanup(func() { _ = hostOut.Close(); _ = hostIn.Close() })
	lines := make(chan string, 64)
	go func() {
		scan := bufio.NewScanner(hostIn)
		for scan.Scan() {
			lines <- scan.Text()
		}
		close(lines)
	}()
	send := func(line string) { go func() { _, _ = io.WriteString(hostOut, line+"\n") }() }
	send(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/","mcpServers":[]%s}}`, askForFrames))
	nextLine(t, lines)
	prompt := func(id int, text string) []string {
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"session/prompt","params":{"sessionId":%q,"prompt":[{"type":"text","text":%q}]}}`, id, fix.SessionID(), text))
		var got []string
		for {
			line := nextLine(t, lines)
			got = append(got, line)
			if strings.Contains(line, `"stopReason"`) {
				return got
			}
		}
	}
	held := `"task_id":"` + HeldTaskID + `"`

	if turn := strings.Join(prompt(2, PromptHold), "\n"); !strings.Contains(turn, held) || !strings.Contains(turn, `"tasks":[{`) {
		t.Fatalf("hold turn does not list %s as live:\n%s", HeldTaskID, turn)
	}
	select {
	case line := <-lines:
		t.Fatalf("held task changed without a release: %s", line)
	case <-time.After(4 * BackgroundDelay):
	}

	release := strings.Join(prompt(3, PromptRelease), "\n")
	if strings.Contains(release, `"subtype":"task_notification"`) {
		t.Fatalf("task ended inside the release turn:\n%s", release)
	}
	var after []string
	timeout := time.After(time.Second)
	for !strings.Contains(strings.Join(after, "\n"), "background done") {
		select {
		case line := <-lines:
			after = append(after, line)
		case <-timeout:
			t.Fatalf("no follow-up after release:\n%s", strings.Join(after, "\n"))
		}
	}
	if end := strings.Join(after, "\n"); !strings.Contains(end, `"subtype":"task_notification"`) || !strings.Contains(end, held) {
		t.Fatalf("release did not end %s:\n%s", HeldTaskID, end)
	}
}

func nextLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("agent output ended")
		}
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for agent output")
		return ""
	}
}
