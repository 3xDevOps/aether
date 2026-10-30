//go:build integration

package server

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/quota"
	"github.com/3xDevOps/Aether/internal/store"
)

// stubClaudeUsage reports each account's used share of its Claude five-hour
// window; an account it has no figure for reads 0%.
type stubClaudeUsage struct {
	mu   sync.Mutex
	used map[domain.MemberID]float64
}

func (u *stubClaudeUsage) set(account domain.MemberID, used float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.used[account] = used
}

func (u *stubClaudeUsage) Read(_ context.Context, account domain.MemberID, _ bool) ([]quota.Provider, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	resets := time.Now().Add(time.Hour)
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{
		{ID: "five_hour", UsedPercent: u.used[account], ResetsAt: &resets},
	}}}, nil
}

// TestIntegrationLaunchFallsBackFromExhaustedAccount: Bo shares his account
// with Ada. Bo's usage is exhausted, so a launch that asks for his account
// starts on Ada's own; the run records Bo as the account Ada asked for.
func TestIntegrationLaunchFallsBackFromExhaustedAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	usage := &stubClaudeUsage{used: map[domain.MemberID]float64{}}
	e := &coordEnv{rt: newE2ERuntime(), image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	if _, err := srv.srv.Store().ShareAccount(ctx, e.bo.id, e.ada.id); err != nil {
		t.Fatalf("share Bo's account with Ada: %v", err)
	}
	usage.set(e.bo.id, 100)
	usage.set(e.ada.id, 10)

	adaCtrl, adaClient := srv.control(t, e.ada.key)
	defer adaClient.Close()
	var launched protocol.RunResult
	if err := adaCtrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "fallback launch", Harness: "claude",
		Mode: string(domain.LaunchHeadless), AccountMemberID: string(e.bo.id),
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	if launched.Run.AccountMemberID != string(e.ada.id) || launched.Run.RequestedAccountMemberID != string(e.bo.id) {
		t.Fatalf("run account %s requested %s; want Ada %s requested Bo %s",
			launched.Run.AccountMemberID, launched.Run.RequestedAccountMemberID, e.ada.id, e.bo.id)
	}
}

// TestIntegrationHeadlessFallback: Ada runs headless
// Claude on Bo's shared account. Its output reports Bo's usage limit and it
// exits; the run continues on Ada's own account, which has usage left, in a
// new container that resumes the agent's session, and its room says so.
// When the resumed agent then fails for another reason, the run fails with
// that exit: the adapter reads only the new session's output, never the
// first agent's limit again. Enhanced runs take the same move from their
// failed prompt; this runtime has no ACP exec, so the scheduler suite covers
// them.
func TestIntegrationHeadlessFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	usage := &stubClaudeUsage{used: map[domain.MemberID]float64{}}
	rt := newE2ERuntime()
	e := &coordEnv{rt: rt, image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	if _, err := srv.srv.Store().ShareAccount(ctx, e.bo.id, e.ada.id); err != nil {
		t.Fatalf("share Bo's account with Ada: %v", err)
	}
	login := filepath.Join(e.dataDir, "homes", string(e.bo.id), ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(login), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(login, []byte("bo-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	usage.set(e.bo.id, 50)
	usage.set(e.ada.id, 10)
	limit, err := os.ReadFile(filepath.Join("..", "adapter", "testdata", "claude_usage_limit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	const task = "run until the usage limit"
	rt.script(task, func(c *e2eContainer) {
		c.output(strings.ReplaceAll(string(limit), "\n", "\r\n"))
		time.Sleep(500 * time.Millisecond)
		c.exitNow(1)
	})
	// The resumed agent fails for another reason once the adapter has tapped
	// its session: the first agent's usage limit, still in the run's
	// transcript, must not be read as this one's.
	rt.script("Your previous turn stopped because Bo's usage ran out. The run continues on Ada's account. Continue the task where you stopped.", func(c *e2eContainer) {
		c.output("resumed\r\n")
		time.Sleep(time.Second)
		c.exitNow(1)
	})

	ctrl, client := srv.control(t, e.ada.key)
	defer client.Close()
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: task, Harness: "claude",
		Mode: string(domain.LaunchHeadless), AccountMemberID: string(e.bo.id),
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	if launched.Run.AccountMemberID != string(e.bo.id) {
		t.Fatalf("launched on %s, want Bo's account", launched.Run.AccountMemberID)
	}
	runID := domain.RunID(launched.Run.ID)
	first := rt.container(launched.Run.ID)

	var run protocol.RunResult
	deadline := time.Now().Add(time.Minute)
	for {
		if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: launched.Run.ID}, &run); err != nil {
			t.Fatalf("run.get: %v", err)
		}
		if run.Run.AccountMemberID == string(e.ada.id) {
			break
		}
		if run.Run.Status != string(domain.RunRunning) || time.Now().After(deadline) {
			t.Fatalf("run %s on %s (%s); want it continued on Ada's account", run.Run.Status, run.Run.AccountMemberID, run.Run.Reason)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if run.Run.Status != string(domain.RunRunning) || run.Run.RequestedAccountMemberID != string(e.bo.id) {
		t.Fatalf("moved run %s requested %s; want running, requested on Bo's account", run.Run.Status, run.Run.RequestedAccountMemberID)
	}
	var moved *e2eContainer
	for moved == nil || moved == first {
		moved = rt.container(launched.Run.ID)
		if time.Now().After(deadline) {
			t.Fatal("no container replaced the exhausted one")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.Contains(moved.spec.Command, "s1") || moved.spec.Env["AETHER_ACCOUNT_MEMBER_ID"] != string(e.ada.id) {
		t.Fatalf("the run's container after the move: %+v", moved.spec)
	}
	for noted := false; !noted; {
		page, err := srv.srv.Store().ListRoomMessages(ctx, e.ws.ID, runID, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		noted = slices.ContainsFunc(page.Items, func(m *store.RoomMessage) bool {
			return m.Kind == store.RoomMessageSystem && m.Body == "Continued on Ada's account because Bo's usage ran out."
		})
		if !noted && time.Now().After(deadline) {
			t.Fatal("the room has no note of the move")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for {
		if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: launched.Run.ID}, &run); err != nil {
			t.Fatalf("run.get: %v", err)
		}
		if run.Run.Status == string(domain.RunFailed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s (%s); want the resumed agent's failure", run.Run.Status, run.Run.Reason)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.HasPrefix(run.Run.Reason, "Usage limit reached") || run.Run.AccountMemberID != string(e.ada.id) {
		t.Fatalf("resumed run failed with %q on %s; want its own exit on Ada's account", run.Run.Reason, run.Run.AccountMemberID)
	}
}
