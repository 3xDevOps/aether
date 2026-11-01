// Package eval measures how the built-in assistant behaves on synthetic
// prompts. It serves the assistant's real tool registry over MCP from a fake
// control surface, so a real agent can be driven against scenario state while
// every tool call is recorded and no confirmed call changes anything.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/docs"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ops"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type ToolCall struct {
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args"`
	Error bool            `json:"error,omitempty"`
}

// Recorder collects the tool calls of one run.
type Recorder struct {
	mu    sync.Mutex
	calls []ToolCall
}

func (r *Recorder) add(c ToolCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *Recorder) Calls() []ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// confirmed are the methods the assistant gates behind an approval card; the
// fake records them and reports success without doing anything.
var confirmed = map[string]bool{
	protocol.MethodRunLaunch: true, protocol.MethodRunInject: true, protocol.MethodRunKill: true,
	protocol.MethodRunClose: true, protocol.MethodRunRelaunch: true, protocol.MethodRunPRMerge: true,
	protocol.MethodRunPRCreate: true, protocol.MethodMemoryAdd: true,
	protocol.MethodEnvSave: true, protocol.MethodRunInputRespond: true,
}

func call(s Scenario) ops.Call {
	var mu sync.Mutex
	var started []protocol.Run
	return func(_ context.Context, method string, params, out any) error {
		mu.Lock()
		defer mu.Unlock()
		var raw json.RawMessage
		switch {
		case method == protocol.MethodServerInfo:
			raw, _ = json.Marshal(protocol.ServerInfoResult{Member: protocol.Member{ID: s.Member.ID, DisplayName: s.Member.Name}, DashboardURL: "https://aether.example"})
		case method == protocol.MethodAssistantDocs:
			return docsAnswer(params, out)
		case method == protocol.MethodAssistantViewContext:
			res := protocol.AssistantViewContextResult{Connected: false}
			if len(s.ViewContext) > 0 {
				res.ViewContext, res.Source, res.AgeSeconds, res.Connected = new(protocol.AssistantViewContext), "prompt", 5, true
				if err := json.Unmarshal(s.ViewContext, res.ViewContext); err != nil {
					return fmt.Errorf("eval: view context: %w", err)
				}
			}
			raw, _ = json.Marshal(res)
		case method == protocol.MethodRunLaunch:
			var p protocol.RunLaunchParams
			if err := json.Unmarshal(mustMarshal(params), &p); err != nil {
				return err
			}
			started = append(started, launchedRun(p, len(started)+1))
			raw = mustMarshal(protocol.RunLaunchResult{Run: started[len(started)-1]})
		case method == protocol.MethodRunGet && slices.ContainsFunc(started, func(r protocol.Run) bool { return r.ID == idOf(params) }):
			raw = mustMarshal(protocol.RunResult{Run: started[slices.IndexFunc(started, func(r protocol.Run) bool { return r.ID == idOf(params) })]})
		case method == protocol.MethodRunList && len(started) > 0:
			var list protocol.RunListResult
			if err := json.Unmarshal(s.Answers[method], &list); err != nil {
				return fmt.Errorf("eval: run.list: %w", err)
			}
			raw = mustMarshal(protocol.RunListResult{Runs: append(list.Runs, started...)})
		case s.Answers[method+":"+idOf(params)] != nil:
			raw = s.Answers[method+":"+idOf(params)]
		case s.Answers[method] != nil:
			raw = s.Answers[method]
		case confirmed[method]:
			raw = json.RawMessage(`{}`)
		default:
			return &protocol.Error{Code: protocol.CodeInternal, Message: fmt.Sprintf("%s is not available in this synthetic scenario", method)}
		}
		return json.Unmarshal(raw, out)
	}
}

type anyInput map[string]any

// Server is a running fake tool server for one scenario run.
type Server struct {
	URL      string
	Recorder *Recorder
	http     *httptest.Server
}

func (s *Server) Close() { s.http.Close() }

// NewServer serves the assistant's tools for s over HTTP MCP. Tools named in
// s.Stubs that the registry does not offer yet are served from the scenario's
// fixture, so a scenario can describe a capability before it exists.
func NewServer(s Scenario) *Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "aether", Version: "eval"}, nil)
	ops.RegisterAssistant(srv, call(s))
	rec := &Recorder{}
	for name, stub := range s.Stubs {
		if Offered(name) {
			continue
		}
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: stub.Description}, func(context.Context, *mcp.CallToolRequest, anyInput) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(stub.Result)}}}, nil, nil
		})
	}
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if method == "tools/call" {
				p := req.GetParams().(*mcp.CallToolParamsRaw)
				c := ToolCall{Tool: p.Name, Args: p.Arguments, Error: err != nil}
				if r, ok := res.(*mcp.CallToolResult); ok && r != nil {
					c.Error = c.Error || r.IsError
				}
				rec.add(c)
			}
			return res, err
		}
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(h)
	return &Server{URL: ts.URL, Recorder: rec, http: ts}
}

// Offered reports whether the real tool registry offers the tool to the
// assistant.
func Offered(tool string) bool {
	return slices.ContainsFunc(ops.Ops, func(o ops.Op) bool { return o.Name == tool && o.Tier != ops.TierNone })
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// launchedRun is the run a recorded launch pretends to have started.
func launchedRun(p protocol.RunLaunchParams, n int) protocol.Run {
	return protocol.Run{ID: fmt.Sprintf("run-eval-%d", n), WorkspaceID: p.WorkspaceID, Harness: p.Harness, Mode: p.Mode, Model: p.Model,
		AccountMemberID: p.AccountMemberID, Status: "running", Activity: "working", PendingInputs: []domain.RunInputRequest{}}
}

func idOf(params any) string {
	b, _ := json.Marshal(params)
	var p struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(b, &p)
	return p.RunID
}

// docsAnswer serves Aether's own guides, as the assistant's tool socket does.
func docsAnswer(params, out any) error {
	b, _ := json.Marshal(params)
	var p protocol.AssistantDocsParams
	_ = json.Unmarshal(b, &p)
	var res protocol.AssistantDocsResult
	if p.Topic == "" {
		entries, err := fs.ReadDir(docs.FS, ".")
		if err != nil {
			return err
		}
		for _, e := range entries {
			if name, ok := strings.CutSuffix(e.Name(), ".md"); ok {
				res.Topics = append(res.Topics, name)
			}
		}
	} else {
		body, err := fs.ReadFile(docs.FS, p.Topic+".md")
		if err != nil {
			return &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("assistant.docs: unknown topic %q", p.Topic)}
		}
		res.Text = string(body)
	}
	raw, _ := json.Marshal(res)
	return json.Unmarshal(raw, out)
}
