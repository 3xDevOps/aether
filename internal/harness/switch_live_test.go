//go:build integration

package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
)

// liveAgent drives one agent's terminal CLI in print mode, which reads and
// writes the same session store its TUI does.
type liveAgent struct {
	acp []string
	// model, when set, is the model both sides use.
	model string
	// start runs prompt in a new terminal session and returns its id.
	start func(ctx context.Context, dir, prompt string) (string, error)
	// resume runs prompt in session through the terminal and returns the
	// session it ran in and the agent's reply.
	resume func(ctx context.Context, dir, session, prompt string) (string, string, error)
}

// ompLiveModel picks omp's model when its configured default cannot answer.
var ompLiveModel = os.Getenv("ACP_LIVE_OMP_MODEL")

var liveAgents = map[string]liveAgent{
	"claude": {
		acp: []string{"npx", "-y", claudeACP.Package + "@" + claudeACP.Version},
		start: func(ctx context.Context, dir, prompt string) (string, error) {
			id, _, err := claudePrint(ctx, dir, "-p", "--output-format", "json", prompt)
			return id, err
		},
		resume: func(ctx context.Context, dir, session, prompt string) (string, string, error) {
			return claudePrint(ctx, dir, "-p", "--output-format", "json", "--resume", session, prompt)
		},
	},
	"codex": {
		acp: []string{"npx", "-y", codexACP.Package + "@" + codexACP.Version},
		start: func(ctx context.Context, dir, prompt string) (string, error) {
			id, _, err := codexPrint(ctx, dir, "exec", "--json", "--skip-git-repo-check", prompt)
			return id, err
		},
		resume: func(ctx context.Context, dir, session, prompt string) (string, string, error) {
			return codexPrint(ctx, dir, "exec", "--json", "--skip-git-repo-check", "resume", session, prompt)
		},
	},
	"omp": {
		acp:   []string{"omp", "acp"},
		model: ompLiveModel,
		start: func(ctx context.Context, dir, prompt string) (string, error) {
			id, _, err := ompPrint(ctx, dir, "-p", "--mode", "json", prompt)
			return id, err
		},
		resume: func(ctx context.Context, dir, session, prompt string) (string, string, error) {
			return ompPrint(ctx, dir, "-p", "--mode", "json", "--resume="+session, prompt)
		},
	},
}

// TestLiveSwitch is the check behind Profile.SwitchVerified: a session
// started over the agent's ACP server resumes in its terminal, and one
// started in its terminal resumes over ACP. It runs the CLIs and adapters on
// this host's PATH with their own logins and sends four short prompts per
// agent. Set ACP_LIVE=1 to run.
func TestLiveSwitch(t *testing.T) {
	if os.Getenv("ACP_LIVE") != "1" {
		t.Skip("set ACP_LIVE=1 to switch sessions of the real agents")
	}
	for _, name := range []string{"claude", "codex", "omp"} {
		t.Run(name, func(t *testing.T) {
			p, _ := Lookup(name)
			agent := liveAgents[name]
			for _, bin := range []string{p.TUIArgs[0], agent.acp[0]} {
				if _, err := exec.LookPath(bin); err != nil {
					t.Skipf("%s is not on PATH", bin)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			const ask = "What is the code word? Answer with the word only."

			s := startLiveACP(ctx, t, agent, dir, "")
			enhanced := s.SessionID()
			liveTurn(ctx, t, s, "Remember the code word PELICAN. Reply with just OK.")
			closeLive(t, s)
			resumed, reply, err := agent.resume(ctx, dir, enhanced, ask)
			if err != nil {
				t.Fatalf("resume enhanced session %s in the terminal: %v", enhanced, err)
			}
			t.Logf("terminal resumed %s as %s: %q", enhanced, resumed, reply)
			if resumed != enhanced || !recalled(reply, "PELICAN") {
				t.Fatalf("the terminal did not resume enhanced session %s: ran %s, replied %q", enhanced, resumed, reply)
			}

			standard, err := agent.start(ctx, dir, "Remember the code word OSPREY. Reply with just OK.")
			if err != nil {
				t.Fatalf("start a terminal session: %v", err)
			}
			s = startLiveACP(ctx, t, agent, dir, standard)
			said := liveTurn(ctx, t, s, ask)
			closeLive(t, s)
			t.Logf("ACP resumed terminal session %s as %s: %q", standard, s.SessionID(), said)
			if s.SessionID() != standard || !recalled(said, "OSPREY") {
				t.Fatalf("ACP did not resume terminal session %s: ran %s, replied %q", standard, s.SessionID(), said)
			}
		})
	}
}

func recalled(reply, word string) bool {
	return strings.Contains(strings.ToUpper(reply), word)
}

func startLiveACP(ctx context.Context, t *testing.T, agent liveAgent, dir, session string) *acphost.Session {
	t.Helper()
	cmd := exec.CommandContext(ctx, agent.acp[0], agent.acp[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NO_BROWSER=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
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
	s, err := acphost.Start(ctx, stdout, stdin, acphost.Config{
		LogPath:        filepath.Join(t.TempDir(), "run.items.jsonl"),
		Cwd:            dir,
		SessionID:      session,
		RequireRestore: session != "",
	})
	if err != nil {
		t.Fatalf("open the ACP session: %v\nstderr: %s", err, stderr.String())
	}
	if agent.model != "" {
		if err := s.SetOption(ctx, "model", agent.model); err != nil {
			t.Fatalf("set the model: %v", err)
		}
	}
	return s
}

// liveTurn sends prompt and returns the agent's reply once the turn ends.
func liveTurn(ctx context.Context, t *testing.T, s *acphost.Session, prompt string) string {
	t.Helper()
	before := s.Log().LastSeq()
	if _, err := s.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(prompt)}, false, nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	for {
		items, err := s.Log().ReadAfter(before, 0)
		if err != nil {
			t.Fatal(err)
		}
		var reply strings.Builder
		for _, it := range items {
			switch {
			case it.Kind == acphost.KindMessage && it.Message.Role == "assistant":
				reply.WriteString(it.Message.Text)
			case it.Kind == acphost.KindTurnEnd:
				return reply.String()
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the turn did not end: %v; items %+v", ctx.Err(), items)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func closeLive(t *testing.T, s *acphost.Session) {
	t.Helper()
	_ = s.Close()
	select {
	case <-s.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the ACP server did not close")
	}
}

func runCLI(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w\nstderr: %s\nstdout: %s", strings.Join(argv, " "), err, stderr.String(), out)
	}
	return out, nil
}

func claudePrint(ctx context.Context, dir string, args ...string) (string, string, error) {
	out, err := runCLI(ctx, dir, append([]string{"claude"}, args...)...)
	if err != nil {
		return "", "", err
	}
	var res struct {
		SessionID string `json:"session_id"`
		Result    string `json:"result"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", "", fmt.Errorf("decode claude output %s: %w", out, err)
	}
	return res.SessionID, res.Result, nil
}

func codexPrint(ctx context.Context, dir string, args ...string) (string, string, error) {
	out, err := runCLI(ctx, dir, append([]string{"codex"}, args...)...)
	if err != nil {
		return "", "", err
	}
	var id string
	var reply strings.Builder
	lines := bufio.NewScanner(bytes.NewReader(out))
	for lines.Scan() {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(lines.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "thread.started":
			id = ev.ThreadID
		case ev.Type == "item.completed" && ev.Item.Type == "agent_message":
			reply.WriteString(ev.Item.Text)
		}
	}
	return id, reply.String(), nil
}

func ompPrint(ctx context.Context, dir string, args ...string) (string, string, error) {
	if ompLiveModel != "" {
		args = append([]string{"--model", ompLiveModel}, args...)
	}
	out, err := runCLI(ctx, dir, append([]string{"omp"}, args...)...)
	if err != nil {
		return "", "", err
	}
	var id string
	var reply strings.Builder
	lines := bufio.NewScanner(bytes.NewReader(out))
	lines.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for lines.Scan() {
		var ev struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(lines.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "session" && id == "":
			id = ev.ID
		case ev.Type == "message_end" && ev.Message.Role == "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "text" {
					reply.WriteString(c.Text)
				}
			}
		}
	}
	return id, reply.String(), nil
}
