package acphost

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
)

// adoptLog is an item log whose turn 2 is open after turn 1 ended.
func adoptLog(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/run.items.jsonl"
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{{Kind: KindTurnStart, Turn: 1}, {Kind: KindTurnEnd, Turn: 1, StopReason: "end_turn"}, {Kind: KindTurnStart, Turn: 2}} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAdoptionTakesOverTheOpenTurnsPrompt(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	s, _ := startMock(t, m, Config{LogPath: adoptLog(t), Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 2,
	}})
	waitFor(t, "the adopted prompt", func() bool { return m.called(acpkeeper.AdoptMethod) })
	if m.called(acp.AgentMethodInitialize) || m.called(acp.AgentMethodSessionPrompt) {
		t.Fatal("the adoption initialized the agent or prompted it again")
	}
	_ = s
}

func TestAdoptionInterruptsATurnWhosePromptNeverReachedTheAgent(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	// The keeper still holds turn 1's prompt, answered but not acked.
	s, _ := startMock(t, m, Config{LogPath: adoptLog(t), Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 1,
	}})
	its := items(t, s)
	if !slices.ContainsFunc(its, func(it Item) bool {
		return it.Kind == KindTurnEnd && it.Turn == 2 && it.StopReason == "interrupted"
	}) {
		t.Fatalf("turn 2 did not end interrupted: %+v", its)
	}
	if m.called(acpkeeper.AdoptMethod) {
		t.Fatal("turn 1's answer was adopted as turn 2's")
	}
}

// pendingLog is an item log whose open turn 1 left a permission request
// unanswered.
func pendingLog(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/run.items.jsonl"
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	req := &Request{ID: "req-1", Kind: RequestPermission, Title: "rm -rf build", ToolCallID: "t1", Status: RequestPending,
		Options: []Option{{ID: "allow", Name: "Allow", Kind: "allow_once"}, {ID: "reject", Name: "Reject", Kind: "reject_once"}}}
	for _, it := range []Item{{Kind: KindTurnStart, Turn: 1}, {Kind: KindRequest, Turn: 1, Request: req}} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnAnswerBeforeTheKeepersReplayReachesTheAgent(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	s, _ := startMock(t, m, Config{LogPath: pendingLog(t), Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 1, ReplayWait: time.Minute,
	}})
	if err := s.Answer("req-1", "allow", nil); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("answer without waiting for the replay = %v", err)
	}
	answered := make(chan error, 1)
	go func() {
		<-s.Replayed()
		answered <- s.Answer("req-1", "allow", nil)
	}()
	select {
	case err := <-answered:
		t.Fatalf("the answer did not wait for the replay: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	got := make(chan string, 1)
	go func() {
		res, err := m.request(context.Background(), acp.ClientMethodSessionRequestPermission, m.permissionRequest())
		if err != nil {
			got <- err.Error()
			return
		}
		got <- string(res)
	}()
	if err := <-answered; err != nil {
		t.Fatalf("answer after the replay: %v", err)
	}
	if res := <-got; res != `{"outcome":{"optionId":"allow","outcome":"selected"}}` {
		t.Fatalf("agent got %s", res)
	}
}

func TestAReplayThatNeverComesStopsHoldingAnswers(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	s, _ := startMock(t, m, Config{LogPath: pendingLog(t), Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 1, ReplayWait: 50 * time.Millisecond,
	}})
	select {
	case <-s.Replayed():
	case <-time.After(5 * time.Second):
		t.Fatal("the replay wait never ended")
	}
	if err := s.Answer("req-1", "allow", nil); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("answer to a request the keeper never sent again = %v", err)
	}
}

func TestASessionThatWasNotAdoptedIsReplayed(t *testing.T) {
	s, _ := startMock(t, newMockAgent(t, loadFixture(t, "claude")), Config{})
	select {
	case <-s.Replayed():
	default:
		t.Fatal("a new session waits for a replay")
	}
}
