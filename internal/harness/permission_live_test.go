//go:build integration

package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
)

// TestLiveEnhancedRunsWithoutPermissionPrompts opens each agent's enhanced
// session as the scheduler does and has it run a shell command; any
// session/request_permission fails it. Set ACP_LIVE=1 to run.
func TestLiveEnhancedRunsWithoutPermissionPrompts(t *testing.T) {
	if os.Getenv("ACP_LIVE") != "1" {
		t.Skip("set ACP_LIVE=1 to run the real agents")
	}
	for _, name := range []string{"claude", "omp"} {
		t.Run(name, func(t *testing.T) {
			p, _ := Lookup(name)
			agent := liveAgents[name]
			if _, err := exec.LookPath(agent.acp[0]); err != nil {
				t.Skipf("%s is not on PATH", agent.acp[0])
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			s := startLiveACP(ctx, t, agent, dir, "")
			defer closeLive(t, s)
			if p.ACPMode != "" {
				if err := s.SetMode(ctx, p.ACPMode); err != nil {
					t.Fatalf("set mode %s: %v", p.ACPMode, err)
				}
			}
			t.Logf("session %s in mode %q", s.SessionID(), s.State().Mode)

			before := s.Log().LastSeq()
			if _, err := s.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(
				"Use your shell tool to run `echo ok > ran.txt`, then reply with the word done.")}, false, nil); err != nil {
				t.Fatalf("prompt: %v", err)
			}
			end := awaitTurnWithoutRequests(ctx, t, s, before)
			if end.StopReason != "end_turn" {
				t.Fatalf("the turn ended %q, want end_turn", end.StopReason)
			}
			if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err != nil {
				t.Fatalf("the shell command did not run: %v", err)
			}
		})
	}
}

func awaitTurnWithoutRequests(ctx context.Context, t *testing.T, s *acphost.Session, after int64) acphost.Item {
	t.Helper()
	for {
		items, err := s.Log().ReadAfter(after, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			switch it.Kind {
			case acphost.KindRequest:
				t.Fatalf("the agent asked for permission: %+v", it.Request)
			case acphost.KindTurnEnd:
				return it
			}
		}
		if len(items) > 0 {
			after = items[len(items)-1].Seq
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the turn did not end: %v", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}
