//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/connector"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// localEdge stands in for a connected edge at origin, which is all
// connector.code and the connector URL need.
type localEdge struct {
	sshd.EdgeOwner
	origin string
}

func (e localEdge) Origin() string { return e.origin }
func (localEdge) Connected() bool  { return true }
func (localEdge) EdgeVersion() int { return 2 }

var consentRequest = regexp.MustCompile(`name="request" value="([^"]+)"`)

func TestIntegrationConnectorFlows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	env := startEnhancedServer(ctx, t)
	defer env.stop(t)
	clients := http.NewServeMux()
	client := httptest.NewUnstartedServer(clients)
	host := client.Listener.Addr().String()
	clientID, redirect := "https://"+host+"/client.json", "https://"+host+"/callback"
	doc, _ := json.Marshal(map[string]any{"client_id": clientID, "client_name": "Test client", "redirect_uris": []string{redirect}})
	clients.HandleFunc("GET /client.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	})
	client.StartTLS()
	t.Cleanup(client.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	env.srv.ssh.SetEdgeOwner(localEdge{origin: "http://" + ln.Addr().String()})
	h, err := connector.New(connector.Config{BaseURL: env.srv.ssh.ConnectorURL(), Store: env.srv.db, SSH: env.srv.ssh,
		KeyPath: filepath.Join(env.data, "connector", "hmac.key"), ClientHosts: []string{host}, Transport: client.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	mcpSrv := httptest.NewUnstartedServer(h)
	_ = mcpSrv.Listener.Close()
	mcpSrv.Listener = ln
	mcpSrv.Start()
	t.Cleanup(mcpSrv.Close)

	var code protocol.ConnectorCodeResult
	if status := postJSON(t, env.web+"/api/v1/"+protocol.MethodConnectorCode, "{}", &code); status != http.StatusOK {
		t.Fatalf("connector.code status %d", status)
	}
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: clientID},
		RedirectURL:                    redirect,
		AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			resp, err := browser.Get(args.URL)
			if err != nil {
				return nil, err
			}
			page, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			m := consentRequest.FindSubmatch(page)
			if m == nil {
				return nil, fmt.Errorf("consent page %s: %s", resp.Status, page)
			}
			if resp, err = browser.PostForm(args.URL, url.Values{"request": {string(m[1])}, "code": {code.Code}, "scope": {"write", "control"}}); err != nil {
				return nil, err
			}
			_ = resp.Body.Close()
			loc, err := resp.Location()
			if err != nil {
				return nil, fmt.Errorf("allow %s: %w", resp.Status, err)
			}
			q := loc.Query()
			return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
		},
		RequestRefreshToken: true,
		NewTokenSource: func(_ context.Context, _ *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(tok), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: env.srv.ssh.ConnectorURL(), OAuthHandler: handler, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	tool := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if res.IsError {
			t.Fatalf("%s(%v) failed: %s", name, args, text)
		}
		t.Logf("%s %v\n  -> %.400s", name, args, text)
		var out map[string]any
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	ws := string(env.ws.ID)
	tool("aether_list_workspaces", nil)
	tool("aether_list_agents", nil)

	tasks := []string{"update the changelog", "fix the flaky login test", "add rate limiting", "clean the build directory"}
	ids := make([]string, len(tasks))
	launched := make([]*mcp.CallToolResult, len(tasks))
	errs := make([]error, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Go(func() {
			launched[i], errs[i] = session.CallTool(ctx, &mcp.CallToolParams{Name: "aether_launch_run", Arguments: map[string]any{
				"workspace_id": ws, "harness": "fake", "mode": "acp", "task": task, "idempotency_key": fmt.Sprintf("launch-%d", i)}})
		})
	}
	wg.Wait()
	for i, res := range launched {
		if errs[i] != nil || res.IsError {
			t.Fatalf("launch of %q: %v %+v", tasks[i], errs[i], res)
		}
		t.Logf("aether_launch_run %q\n  -> %s", tasks[i], res.Content[0].(*mcp.TextContent).Text)
		ids[i], _ = res.StructuredContent.(map[string]any)["run_id"].(string)
		if ids[i] == "" {
			t.Fatalf("launch of %q returned no run id: %+v", tasks[i], res.StructuredContent)
		}
	}
	if again := tool("aether_launch_run", map[string]any{"workspace_id": ws, "harness": "fake", "mode": "acp", "task": tasks[1],
		"idempotency_key": "launch-1"}); again["run_id"] != ids[1] || again["replayed"] != true {
		t.Fatalf("launch replayed under its key = %v, want run %s", again, ids[1])
	}
	for _, id := range ids {
		waitTurns(ctx, t, env.srv, domain.RunID(id), 1)
	}

	active := tool("aether_list_runs", map[string]any{"active_only": true})["runs"].([]any)
	if len(active) != len(tasks) {
		t.Fatalf("active runs = %v, want the %d launched", active, len(tasks))
	}

	found := tool("aether_list_runs", map[string]any{"query": "rate limiting"})["runs"].([]any)
	if len(found) != 1 || found[0].(map[string]any)["id"] != ids[2] {
		t.Fatalf("runs working on rate limiting = %v, want %s", found, ids[2])
	}
	tool("aether_message_run", map[string]any{"run_id": ids[2], "message": "use a token bucket", "idempotency_key": "msg-1"})
	waitPrompted(ctx, t, env.srv, domain.RunID(ids[2]), "use a token bucket")
	tool("aether_message_run", map[string]any{"run_id": ids[3], "message": acpmock.PromptAskPermission, "idempotency_key": "msg-2"})

	var permission map[string]any
	for permission == nil {
		for _, it := range tool("aether_needs_attention", nil)["items"].([]any) {
			if it := it.(map[string]any); it["run_id"] == ids[3] && it["answer_with"] == "aether_decide_approval" {
				permission = it
			}
		}
		if permission == nil {
			select {
			case <-ctx.Done():
				t.Fatal("the permission request never needed attention")
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	tool("aether_decide_approval", map[string]any{"run_id": ids[3], "answer_id": permission["answer_id"], "decision": "allow",
		"action": permission["untrusted"].(map[string]any)["text"]})
	waitTurns(ctx, t, env.srv, domain.RunID(ids[3]), 2)

	if paused := tool("aether_pause_run", map[string]any{"run_id": ids[1]}); paused["paused"] != true {
		t.Fatalf("aether_pause_run = %v, want paused", paused)
	}
	if got := tool("aether_get_run", map[string]any{"run_id": ids[1]}); got["paused"] != true {
		t.Fatalf("aether_get_run after pause = %v, want paused", got)
	}
	if resumed := tool("aether_resume_run", map[string]any{"run_id": ids[1]}); resumed["paused"] != false {
		t.Fatalf("aether_resume_run = %v, want not paused", resumed)
	}
	if got := tool("aether_get_run", map[string]any{"run_id": ids[1]}); got["paused"] != false {
		t.Fatalf("aether_get_run after resume = %v, want not paused", got)
	}
	tool("aether_run_history", map[string]any{"run_id": ids[1]})

	swarm := tool("aether_launch_swarm", map[string]any{"workspace_id": ws, "objective": "add audit logging", "harness": "fake", "mode": "acp",
		"idempotency_key": "swarm-1"})
	swarmID, integratorRun := swarm["swarm_id"].(string), swarm["integrator_run_id"].(string)
	tool("aether_get_swarm", map[string]any{"swarm_id": swarmID})
	integrator := waitMissionSocket(t, filepath.Join(env.data, "coord", integratorRun))
	var asked protocol.MissionQuestionResult
	if err := pacedCall(ctx, integrator, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "log to a file or to the database?", IdempotencyKey: "ask"}, &asked); err != nil {
		t.Fatalf("mission.question.ask: %v", err)
	}
	var question map[string]any
	for _, it := range tool("aether_needs_attention", nil)["items"].([]any) {
		if it := it.(map[string]any); it["answer_id"] == asked.Question.ID {
			question = it
		}
	}
	if question == nil {
		t.Fatalf("the integrator's question %s does not need attention", asked.Question.ID)
	}
	tool("aether_answer", map[string]any{"run_id": question["run_id"], "answer_id": asked.Question.ID, "answer": "the database"})
	tool("aether_list_swarms", nil)

	for _, id := range ids {
		tool("aether_stop_run", map[string]any{"run_id": id})
	}
	tool("aether_cancel_swarm", map[string]any{"swarm_id": swarmID})
	for _, id := range append(ids, integratorRun) {
		for {
			r := tool("aether_get_run", map[string]any{"run_id": id})["run"].(map[string]any)
			if r["state"] != "running" && r["state"] != "needs-attention" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("run %s is still %s after it was stopped", id, r["state"])
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	summary := tool("aether_activity_summary", map[string]any{"period": "today"})
	if raw, _ := json.Marshal(summary); !strings.Contains(string(raw), ids[2]) {
		t.Fatalf("today's activity = %s, want the runs launched today", raw)
	}
}
