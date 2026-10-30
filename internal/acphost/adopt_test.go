package acphost

import (
	"slices"
	"testing"

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
