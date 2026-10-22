// Package tools serves the assistant's member-scoped tool socket: the
// allowlisted subset of the control surface the assistant may call, graded
// into tiers, with approval gating on the destructive ones.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/docs"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	maxRequestBytes = 64 << 10
	maxConns        = 16
	idleTimeout     = 5 * time.Minute
)

const (
	tierRead       = "read"
	tierReversible = "reversible"
	tierConfirmed  = "confirmed"
)

var tiers = map[string]string{
	protocol.MethodRunList:              tierRead,
	protocol.MethodRunGet:               tierRead,
	protocol.MethodRunACPHistory:        tierRead,
	protocol.MethodWorkspaceList:        tierRead,
	protocol.MethodWorkspaceGet:         tierRead,
	protocol.MethodAgentList:            tierRead,
	protocol.MethodAccountUsage:         tierRead,
	protocol.MethodTerminalStatus:       tierRead,
	protocol.MethodAssistantDocs:        tierRead,
	protocol.MethodAssistantViewContext: tierRead,
	protocol.MethodMemorySearch:         tierRead,
	protocol.MethodRunLaunch:            tierConfirmed,
	protocol.MethodRunInject:            tierConfirmed,
	protocol.MethodRunPause:             tierReversible,
	protocol.MethodRunResume:            tierReversible,
	protocol.MethodRunKill:              tierConfirmed,
	protocol.MethodEnvSave:              tierConfirmed,
}

type Caller interface {
	Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error)
}

type Confirmer func(ctx context.Context, c protocol.AssistantConfirmation) error

type Server struct {
	Caller  Caller
	Confirm Confirmer
	// Member is the member the socket belongs to; "mine" in a run.list
	// request selects their runs.
	Member string
	Follow func(ctx context.Context, runID string)
	// View is the member's latest view context.
	View func() protocol.AssistantViewContextResult
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	slots := make(chan struct{}, maxConns)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("assistant tools: accept: %w", err)
		}
		select {
		case slots <- struct{}{}:
		default:
			slog.Warn("assistant tools: connection refused at the concurrency cap", "cap", maxConns)
			_ = conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer func() {
				<-slots
				wg.Done()
			}()
			s.serve(ctx, conn)
		}()
	}
}

func (s *Server) serve(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	capped := &capReader{r: conn}
	r := bufio.NewReaderSize(capped, 16<<10)
	for {
		capped.left = maxRequestBytes
		if conn.SetReadDeadline(time.Now().Add(idleTimeout)) != nil {
			return
		}
		line, err := protocol.ReadLine(r)
		if err != nil {
			return
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		req, resp, ok := protocol.ParseRequest(line)
		if ok {
			result, rpcErr := s.dispatchWatched(ctx, conn, r, req)
			if rpcErr != nil {
				resp.Error = rpcErr
			} else {
				resp.Result = result
			}
		}
		out, err := json.Marshal(resp)
		if err != nil || conn.SetWriteDeadline(time.Now().Add(idleTimeout)) != nil {
			return
		}
		if _, err := conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// dispatchWatched runs req with a context that ends when the caller hangs up,
// so a confirmation nobody can answer any more is withdrawn. The wait for a
// confirmation outlasts the idle deadline, which is lifted meanwhile.
func (s *Server) dispatchWatched(ctx context.Context, conn net.Conn, r *bufio.Reader, req protocol.Request) (json.RawMessage, *protocol.Error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	_ = conn.SetReadDeadline(time.Time{})
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		if _, err := r.Peek(1); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			cancel()
		}
	}()
	result, rpcErr := s.dispatch(ctx, req)
	_ = conn.SetReadDeadline(time.Now())
	<-watching
	return result, rpcErr
}

func (s *Server) dispatch(ctx context.Context, req protocol.Request) (json.RawMessage, *protocol.Error) {
	tier, allowed := tiers[req.Method]
	if !allowed {
		slog.Info("assistant tool", "method", req.Method, "tier", "none", "outcome", "method_not_found")
		return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: fmt.Sprintf(
			"method not found: %s; allowed methods: %s", req.Method, strings.Join(allowedMethods(), ", "))}
	}
	result, rpcErr := s.run(ctx, req, tier)
	outcome := "ok"
	if rpcErr != nil {
		outcome = fmt.Sprintf("error %d", rpcErr.Code)
	}
	slog.Info("assistant tool", "method", req.Method, "tier", tier, "outcome", outcome)
	return result, rpcErr
}

func (s *Server) run(ctx context.Context, req protocol.Request, tier string) (json.RawMessage, *protocol.Error) {
	params, err := Prepare(s.Member, req.Method, req.Params)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
	}
	req.Params = params
	switch {
	case req.Method == protocol.MethodAssistantDocs:
		return serveDocs(req.Params)
	case req.Method == protocol.MethodAssistantViewContext:
		return s.serveView()
	case tier == tierConfirmed:
		c := protocol.AssistantConfirmation{Method: req.Method, Params: req.Params, CreatedAt: time.Now().UTC()}
		if rpcErr := s.summarize(ctx, req, &c); rpcErr != nil {
			return nil, rpcErr
		}
		if err := s.Confirm(ctx, c); err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) {
				return nil, pe
			}
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: req.Method + " was not approved: " + err.Error()}
		}
	}
	result, rpcErr := s.Caller.Call(ctx, req.Method, req.Params)
	if rpcErr == nil && s.Follow != nil {
		if id := s.followed(req, result); id != "" {
			s.Follow(context.WithoutCancel(ctx), id)
		}
	}
	return result, rpcErr
}

func (s *Server) followed(req protocol.Request, result json.RawMessage) string {
	switch req.Method {
	case protocol.MethodRunLaunch:
		launched, err := protocol.DecodeParams[protocol.RunResult](result)
		if err != nil {
			slog.Warn("assistant tools: read the launched run", "error", err)
			return ""
		}
		return launched.Run.ID
	case protocol.MethodRunInject:
		p, err := protocol.DecodeParams[protocol.RunInjectParams](req.Params)
		if err != nil {
			return ""
		}
		return p.RunID
	}
	return ""
}

func (s *Server) summarize(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) (rpcErr *protocol.Error) {
	switch req.Method {
	case protocol.MethodEnvSave:
		c.Summary = "Save your environment as a new image; new runs and terminals start from it"
		return nil
	case protocol.MethodRunLaunch:
		c.Summary, rpcErr = s.summarizeLaunch(ctx, req)
		return rpcErr
	case protocol.MethodRunInject:
		return s.summarizeInject(ctx, req, c)
	}
	p, err := protocol.DecodeParams[protocol.RunIDParams](req.Params)
	if err != nil || p.RunID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
	}
	run, rpcErr := s.getRun(ctx, req.Params)
	if rpcErr != nil {
		return rpcErr
	}
	if rpcErr = s.owner(ctx, run, c); rpcErr != nil {
		return rpcErr
	}
	c.Summary = fmt.Sprintf("Stop run %s (%q)%s", p.RunID, runLabel(run), ownedBy(c))
	return nil
}

func (s *Server) summarizeLaunch(ctx context.Context, req protocol.Request) (string, *protocol.Error) {
	p, err := protocol.DecodeParams[protocol.RunLaunchParams](req.Params)
	if err != nil || p.WorkspaceID == "" || p.Harness == "" {
		return "", &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": workspace_id and harness are required"}
	}
	params, _ := json.Marshal(protocol.WorkspaceGetParams{WorkspaceID: p.WorkspaceID})
	raw, rpcErr := s.Caller.Call(ctx, protocol.MethodWorkspaceGet, params)
	if rpcErr != nil {
		return "", rpcErr
	}
	var ws protocol.WorkspaceGetResult
	if err := json.Unmarshal(raw, &ws); err != nil {
		return "", &protocol.Error{Code: protocol.CodeInternal, Message: "decode workspace.get result: " + err.Error()}
	}
	mode := p.Mode
	if mode == "" {
		mode = "default mode"
	}
	return fmt.Sprintf("Launch %s (%s) in %s: %q", p.Harness, mode, ws.Workspace.Name, truncate(p.Task)), nil
}

func (s *Server) summarizeInject(ctx context.Context, req protocol.Request, c *protocol.AssistantConfirmation) *protocol.Error {
	p, err := protocol.DecodeParams[protocol.RunInjectParams](req.Params)
	if err != nil || p.RunID == "" {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
	}
	params, _ := json.Marshal(protocol.RunIDParams{RunID: p.RunID})
	run, rpcErr := s.getRun(ctx, params)
	if rpcErr != nil {
		return rpcErr
	}
	if rpcErr = s.owner(ctx, run, c); rpcErr != nil {
		return rpcErr
	}
	c.Summary = fmt.Sprintf("Send to run %s (%q)%s: %q", p.RunID, runLabel(run), ownedBy(c), truncate(p.Message))
	return nil
}

// owner marks c as acting on another member's run when the run is not the
// socket's member's, and names that member: their display name, else their
// id.
func (s *Server) owner(ctx context.Context, run protocol.Run, c *protocol.AssistantConfirmation) *protocol.Error {
	if run.MemberID == s.Member {
		return nil
	}
	c.OtherMember, c.OtherOwner = true, run.MemberID
	raw, rpcErr := s.Caller.Call(ctx, protocol.MethodMemberList, nil)
	if rpcErr != nil {
		return rpcErr
	}
	var members protocol.MemberListResult
	if err := json.Unmarshal(raw, &members); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "decode member.list result: " + err.Error()}
	}
	for _, m := range members.Members {
		if m.ID == run.MemberID && m.DisplayName != "" {
			c.OtherOwner = m.DisplayName
		}
	}
	return nil
}

func ownedBy(c *protocol.AssistantConfirmation) string {
	if !c.OtherMember {
		return ""
	}
	return ", owned by " + c.OtherOwner
}

func (s *Server) getRun(ctx context.Context, params json.RawMessage) (protocol.Run, *protocol.Error) {
	raw, rpcErr := s.Caller.Call(ctx, protocol.MethodRunGet, params)
	if rpcErr != nil {
		return protocol.Run{}, rpcErr
	}
	var got protocol.RunResult
	if err := json.Unmarshal(raw, &got); err != nil {
		return protocol.Run{}, &protocol.Error{Code: protocol.CodeInternal, Message: "decode run.get result: " + err.Error()}
	}
	return got.Run, nil
}

func runLabel(run protocol.Run) string {
	if run.Title != "" {
		return run.Title
	}
	return run.Task
}

const maxSummaryText = 120

func truncate(text string) string {
	if r := []rune(text); len(r) > maxSummaryText {
		return string(r[:maxSummaryText]) + "…"
	}
	return text
}

// Prepare checks the params of a call made for member, by the assistant's
// socket or a connector. Either is bound to one member, so the wire never
// names one, except that run.list takes member_id as a read filter, and
// "mine" there selects member. mission.create makes member the swarm's
// accountable human and launches every agent on their account.
func Prepare(member, method string, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 || string(params) == "null" {
		return params, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, errors.New("params must be an object")
	}
	for name := range fields {
		if strings.Contains(strings.ToLower(name), "member_id") && (method != protocol.MethodRunList || name != "member_id") {
			return nil, fmt.Errorf("%s is not accepted: calls act as the member this socket belongs to", name)
		}
	}
	switch method {
	case protocol.MethodMissionCreate:
		return prepareMission(member, params)
	case protocol.MethodRunList:
	default:
		return params, nil
	}
	var mine bool
	if raw, ok := fields["mine"]; ok {
		if err := json.Unmarshal(raw, &mine); err != nil {
			return nil, errors.New("mine must be a boolean")
		}
		delete(fields, "mine")
	}
	if mine {
		id, _ := json.Marshal(member)
		fields["member_id"] = id
	}
	return json.Marshal(fields)
}

func prepareMission(member string, params json.RawMessage) (json.RawMessage, error) {
	p, err := protocol.DecodeParams[protocol.MissionCreateParams](params)
	if err != nil {
		return nil, err
	}
	named := p.AccountableHumanID != "" || p.Integrator.AccountMemberID != ""
	for _, c := range p.ExecutionChoices {
		named = named || c.AccountMemberID != ""
	}
	if named {
		return nil, errors.New("accountable_human_id and account_member_id are not accepted: calls act as the member this socket belongs to")
	}
	p.AccountableHumanID, p.Integrator.AccountMemberID = member, member
	for i := range p.ExecutionChoices {
		p.ExecutionChoices[i].AccountMemberID = member
	}
	return json.Marshal(p)
}

func serveDocs(raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	p, err := protocol.DecodeParams[protocol.AssistantDocsParams](raw)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "assistant.docs: " + err.Error()}
	}
	topics, err := docTopics()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "assistant.docs: " + err.Error()}
	}
	var out protocol.AssistantDocsResult
	switch {
	case p.Topic == "":
		out.Topics = topics
	case slices.Contains(topics, p.Topic):
		body, rerr := fs.ReadFile(docs.FS, p.Topic+".md")
		if rerr != nil {
			return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "assistant.docs: " + rerr.Error()}
		}
		out.Text = string(body)
	default:
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf(
			"assistant.docs: unknown topic %q; topics: %s", p.Topic, strings.Join(topics, ", "))}
	}
	result, err := json.Marshal(out)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "assistant.docs: " + err.Error()}
	}
	return result, nil
}

func (s *Server) serveView() (json.RawMessage, *protocol.Error) {
	var out protocol.AssistantViewContextResult
	if s.View != nil {
		out = s.View()
	}
	result, err := json.Marshal(out)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "assistant.view_context: " + err.Error()}
	}
	return result, nil
}

func docTopics() ([]string, error) {
	entries, err := fs.ReadDir(docs.FS, ".")
	if err != nil {
		return nil, err
	}
	topics := make([]string, 0, len(entries))
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".md"); ok {
			topics = append(topics, name)
		}
	}
	return topics, nil
}

func allowedMethods() []string {
	names := make([]string, 0, len(tiers))
	for name := range tiers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// capReader fails the read past left bytes so one oversized line cannot
// make the server buffer it.
type capReader struct {
	r    io.Reader
	left int
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, fmt.Errorf("assistant tools: request exceeds %d bytes", maxRequestBytes)
	}
	if len(p) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= n
	return n, err
}
