package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/assistant"
)

// Result is one agent run of one scenario.
type Result struct {
	Scenario string        `json:"scenario"`
	Run      int           `json:"run"`
	Checks   []CheckResult `json:"checks"`
	Flags    Flags         `json:"flags"`
	Pass     bool          `json:"pass"`
	Calls    []ToolCall    `json:"calls"`
	// Briefs are the task or objective of each launch, for manual review.
	Briefs []string `json:"briefs,omitempty"`
	Answer string   `json:"answer"`
	// Model is the model that wrote most of the output, as the harness reports it.
	Model   string        `json:"model,omitempty"`
	Latency time.Duration `json:"latency_ns"`
	// CostUSD is nil when the harness did not report it.
	CostUSD *float64 `json:"cost_usd,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Agent runs one prompt and returns the final reply and its cost, if known.
type Agent func(ctx context.Context, dir, mcpURL, prompt string) (answer, model string, cost *float64, err error)

// Claude drives Claude Code the way the assistant does: the assistant's
// instructions are the working directory's CLAUDE.md and its tools are the
// "aether" MCP server. Claude's own tools are off, so the only way to act is
// through the fake server.
func Claude(model string) Agent {
	return func(ctx context.Context, dir, mcpURL, prompt string) (string, string, *float64, error) {
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"aether": map[string]string{"type": "http", "url": mcpURL}}})
		cfgPath := filepath.Join(dir, "mcp.json")
		if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
			return "", "", nil, err
		}
		args := []string{"-p", prompt, "--output-format", "json", "--mcp-config", cfgPath, "--strict-mcp-config",
			"--tools", "", "--allowedTools", "mcp__aether", "--setting-sources", "project", "--permission-mode", "dontAsk"}
		if model != "" {
			args = append(args, "--model", model)
		}
		cmd := exec.CommandContext(ctx, "claude", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return "", "", nil, fmt.Errorf("claude: %w: %s", err, stderr.String())
		}
		var out struct {
			Result  string   `json:"result"`
			IsError bool     `json:"is_error"`
			Cost    *float64 `json:"total_cost_usd"`
			Usage   map[string]struct {
				Out int `json:"outputTokens"`
			} `json:"modelUsage"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			return "", "", nil, fmt.Errorf("claude: decode result: %w", err)
		}
		if out.IsError {
			return "", "", out.Cost, fmt.Errorf("claude: %s", out.Result)
		}
		used := ""
		for m, u := range out.Usage {
			if used == "" || u.Out > out.Usage[used].Out {
				used = m
			}
		}
		return out.Result, used, out.Cost, nil
	}
}

func prompt(s Scenario) string {
	p := s.Prompt
	if s.History != "" {
		p = s.History + "\n\n" + p
	}
	if len(s.ViewContext) > 0 {
		p = "<aether-view-context>" + string(s.ViewContext) + "</aether-view-context>\n" + p
	}
	return p
}

// RunOnce runs scenario s once against a fresh fake server and scores it.
func RunOnce(ctx context.Context, agent Agent, s Scenario, n int) Result {
	res := Result{Scenario: s.ID, Run: n}
	dir, err := os.MkdirTemp("", "assistant-eval-")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		if werr := os.WriteFile(filepath.Join(dir, f), []byte(assistant.Instructions), 0o600); werr != nil {
			res.Error = werr.Error()
			return res
		}
	}
	srv := NewServer(s)
	defer srv.Close()
	start := time.Now()
	answer, used, cost, err := agent(ctx, dir, srv.URL, prompt(s))
	res.Latency, res.CostUSD, res.Answer, res.Model = time.Since(start), cost, answer, used
	if err != nil {
		res.Error = err.Error()
	}
	calls := srv.Recorder.Calls()
	res.Calls = calls
	for _, l := range launches(calls, false) {
		res.Briefs = append(res.Briefs, l.brief())
	}
	res.Checks, res.Flags = Score(s, Outcome{Calls: calls, Answer: answer})
	res.Pass = err == nil
	for _, c := range res.Checks {
		res.Pass = res.Pass && c.Pass
	}
	return res
}
