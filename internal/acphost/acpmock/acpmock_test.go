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

func TestPromptBackgroundRunsAFollowUpAfterTheTurn(t *testing.T) {
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
	lines := make(chan string)
	go func() {
		scan := bufio.NewScanner(hostIn)
		for scan.Scan() {
			lines <- scan.Text()
		}
		close(lines)
	}()
	turn := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s","prompt":[{"type":"text","text":%q}]}}`+"\n", PromptBackground)
	go func() { _, _ = io.WriteString(hostOut, turn) }()

	var inTurn []string
	for {
		line := nextLine(t, lines)
		inTurn = append(inTurn, line)
		if strings.Contains(line, `"stopReason"`) {
			break
		}
	}
	joined := strings.Join(inTurn, "\n")
	if !strings.Contains(joined, `"backgroundTaskId":"`+BackgroundTaskID+`"`) || !strings.Contains(inTurn[len(inTurn)-1], `"stopReason":"end_turn"`) {
		t.Fatalf("turn output:\n%s", joined)
	}
	chunk := nextLine(t, lines)
	usage := nextLine(t, lines)
	if !strings.Contains(chunk, "agent_message_chunk") || !strings.Contains(chunk, "background done") ||
		!strings.Contains(usage, "usage_update") || !strings.Contains(usage, `"_claude/origin":{"kind":"task-notification"}`) {
		t.Fatalf("follow-up:\n%s\n%s", chunk, usage)
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
