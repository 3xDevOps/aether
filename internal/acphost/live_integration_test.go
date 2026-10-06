//go:build integration

package acphost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveAdapters runs the pinned adapters for initialize and session/new
// only, so no prompt is sent and no login is needed. Set ACP_LIVE=1 to run.
func TestLiveAdapters(t *testing.T) {
	if os.Getenv("ACP_LIVE") != "1" {
		t.Skip("set ACP_LIVE=1 to run the real ACP adapters")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not on PATH")
	}
	for _, pkg := range []string{
		"@agentclientprotocol/claude-agent-acp@0.86.0",
		"@agentclientprotocol/codex-acp@2.1.1",
	} {
		t.Run(pkg, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "npx", "-y", pkg)
			cmd.Stderr = os.Stderr
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = stdin.Close()
				done := make(chan struct{})
				go func() { _ = cmd.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					_ = cmd.Process.Kill()
					<-done
				}
			})

			s, err := Start(ctx, stdout, stdin, Config{
				LogPath: filepath.Join(t.TempDir(), "run.items.jsonl"),
				Cwd:     t.TempDir(),
			})
			if err != nil {
				// A logged-out Codex fails session/new; initialize still ran.
				if strings.Contains(err.Error(), "session/new") && strings.Contains(err.Error(), "Authentication required") {
					t.Skipf("agent not logged in: %v", err)
				}
				t.Fatal(err)
			}
			info := s.Info()
			t.Logf("agent %s %s, auth methods %s", info.Name, info.Version, info.AuthMethods)
			if info.ProtocolVersion != 1 || !info.LoadSession || !info.Resume || !info.List || !info.Steering {
				t.Fatalf("capabilities the host relies on are missing: %+v", info)
			}
			st := s.State()
			if s.SessionID() == "" || st.Mode == "" || !strings.Contains(string(st.ConfigOptions), `"category":"mode"`) {
				t.Fatalf("session %q state %+v", s.SessionID(), st)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-s.Done():
			case <-time.After(30 * time.Second):
				t.Fatal("adapter did not exit on stdin EOF")
			}
		})
	}
}
