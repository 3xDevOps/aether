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
