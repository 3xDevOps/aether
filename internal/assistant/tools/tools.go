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
	protocol.MethodRunList:        tierRead,
	protocol.MethodRunGet:         tierRead,
	protocol.MethodRunACPHistory:  tierRead,
	protocol.MethodWorkspaceList:  tierRead,
	protocol.MethodWorkspaceGet:   tierRead,
	protocol.MethodAgentList:      tierRead,
	protocol.MethodAccountUsage:   tierRead,
	protocol.MethodTerminalStatus: tierRead,
	protocol.MethodAssistantDocs:  tierRead,
	protocol.MethodRunLaunch:      tierReversible,
	protocol.MethodRunInject:      tierReversible,
	protocol.MethodRunPause:       tierReversible,
	protocol.MethodRunResume:      tierReversible,
	protocol.MethodRunKill:        tierConfirmed,
	protocol.MethodEnvSave:        tierConfirmed,
}

// Caller runs a control method with the member's authority.
type Caller interface {
	Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error)
}

// Confirmer blocks until the member decides on c. A nil return is approval.
type Confirmer func(ctx context.Context, c protocol.AssistantConfirmation) error

// Server answers tool calls on a listener.
type Server struct {
	Caller  Caller
	Confirm Confirmer
}

// Serve accepts connections until ctx is done or the listener fails.
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
			result, rpcErr := s.dispatch(ctx, req)
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
	if err := checkParams(req.Params); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
	}
	switch {
	case req.Method == protocol.MethodAssistantDocs:
		return serveDocs(req.Params)
	case tier == tierConfirmed:
		summary, rpcErr := s.summarize(ctx, req)
		if rpcErr != nil {
			return nil, rpcErr
		}
		now := time.Now().UTC()
		if err := s.Confirm(ctx, protocol.AssistantConfirmation{
			Method: req.Method, Summary: summary, Params: req.Params, CreatedAt: now,
		}); err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) {
				return nil, pe
			}
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: req.Method + " was not approved: " + err.Error()}
		}
	}
	return s.Caller.Call(ctx, req.Method, req.Params)
}

func (s *Server) summarize(ctx context.Context, req protocol.Request) (string, *protocol.Error) {
	if req.Method == protocol.MethodEnvSave {
		return "Save your environment as a new image; new runs and terminals start from it", nil
	}
	p, err := protocol.DecodeParams[protocol.RunIDParams](req.Params)
	if err != nil || p.RunID == "" {
		return "", &protocol.Error{Code: protocol.CodeInvalidParams, Message: req.Method + ": run_id is required"}
	}
	raw, rpcErr := s.Caller.Call(ctx, protocol.MethodRunGet, req.Params)
	if rpcErr != nil {
		return "", rpcErr
	}
	var got protocol.RunResult
	if err := json.Unmarshal(raw, &got); err != nil {
		return "", &protocol.Error{Code: protocol.CodeInternal, Message: "decode run.get result: " + err.Error()}
	}
	label := got.Run.Title
	if label == "" {
		label = got.Run.Task
	}
	return fmt.Sprintf("Stop run %s (%q)", p.RunID, label), nil
}

// checkParams requires an object and refuses any member selector: the
// socket is bound to one member, so the wire never names one.
func checkParams(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("params must be an object")
	}
	for name := range fields {
		if strings.Contains(strings.ToLower(name), "member_id") {
			return fmt.Errorf("%s is not accepted: calls act as the member this socket belongs to", name)
		}
	}
	return nil
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
