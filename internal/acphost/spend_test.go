package acphost

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// The session reports the agent's running cost with the turn's tokens, and
// marks a total that counts from zero: in a new session, after /clear, and
// below the previous total, which is reported first. Zero and non-USD totals,
// and tokens without a total, are not reported.
func TestSpendReportsTheRunningCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.items.jsonl")
	spent := make(chan Spend, 16)
	cfg := Config{LogPath: path, ClaudeCode: true, OnSpend: func(sp Spend) { spent <- sp }}
	expect := func(when string, want Spend) {
		t.Helper()
		select {
		case got := <-spent:
			if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || math.Abs(got.Total-want.Total) > 1e-9 || got.FromZero != want.FromZero {
				t.Fatalf("%s: spend %+v, want %+v", when, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: no spend reported", when)
		}
	}
	expectNone := func(when string) {
		t.Helper()
		select {
		case got := <-spent:
			t.Fatalf("%s: spend %+v, want none", when, got)
		default:
		}
	}
	var costs []map[string]any
	turn := func(s *Session, rec *recorder, text string, amounts ...any) {
		t.Helper()
		costs = costs[:0]
		for i := 0; i < len(amounts); i += 2 {
			costs = append(costs, map[string]any{"amount": amounts[i], "currency": amounts[i+1]})
		}
		if _, err := s.Prompt(t.Context(), textPrompt(text), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitIdle(t)
	}
	stop := func(s *Session, m *mockAgent) {
		_ = s.Close()
		_ = m.stdout.Close()
		<-s.Done()
	}
	restore := func(cost float64, failed bool) (*Session, *recorder, *mockAgent) {
		t.Helper()
		m := newMockAgent(t, loadFixture(t, "claude"))
		m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
			for _, c := range costs {
				m.update(map[string]any{"sessionUpdate": "usage_update", "used": 1000, "size": 200000, "cost": c})
			}
			return map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 10, "outputTokens": 1, "totalTokens": 11}}, nil
		}
		if failed {
			m.resume = func() *acp.RequestError { return acp.NewInternalError(nil) }
			m.load = m.resume
		}
		c := cfg
		c.SessionID, c.Cost = m.sessionID(), cost
		s, rec := startMock(t, m, c)
		rec.waitIdle(t)
		return s, rec, m
	}

	m := newMockAgent(t, loadFixture(t, "claude"))
	s, rec := startMock(t, m, cfg)
	expect("new session", Spend{FromZero: true})
	turn(s, rec, "ping")
	expect("recorded Claude turn", Spend{InputTokens: 2 + 10202 + 16905, OutputTokens: 4, Total: 0.3418385})
	stop(s, m)

	s, rec, m = restore(0.3418385, false)
	expectNone("restored session")
	turn(s, rec, "go", 0.5, "USD")
	expect("later turn", Spend{InputTokens: 10, OutputTokens: 1, Total: 0.5})
	turn(s, rec, "go", 0, "USD", 9, "EUR")
	expectNone("zero and EUR")
	turn(s, rec, "go", 0.6, "USD", 0.1, "USD")
	expect("total before a lower one", Spend{Total: 0.6})
	expect("lower total", Spend{InputTokens: 10, OutputTokens: 1, Total: 0.1, FromZero: true})
	m.update(map[string]any{"sessionUpdate": "usage_update", "used": 1000, "size": 200000, "cost": map[string]any{"amount": 0.2, "currency": "USD"}})
	expect("no turn open", Spend{Total: 0.2})
	turn(s, rec, "/clear")
	expect("/clear", Spend{FromZero: true})
	turn(s, rec, "go", 0.3, "USD")
	expect("first total after /clear", Spend{InputTokens: 10, OutputTokens: 1, Total: 0.3})
	stop(s, m)

	s, rec, m = restore(0.5, false)
	turn(s, rec, "go", 0.4, "USD")
	expect("restored total below the charged one", Spend{InputTokens: 10, OutputTokens: 1, Total: 0.4, FromZero: true})
	stop(s, m)

	s, rec, _ = restore(0.5, true)
	expect("failed restore", Spend{FromZero: true})
	turn(s, rec, "go", 0.4, "USD")
	expect("new session's turn", Spend{InputTokens: 10, OutputTokens: 1, Total: 0.4})
}
