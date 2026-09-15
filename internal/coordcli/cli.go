// Package coordcli implements the thin aether-internal command surface.
//
// It has no identity, login, or credential options. The mounted coordination
// socket is the caller's identity, and every command is only a small adapter
// over the same v3 service used by the MCP bridge.
package coordcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/3xDevOps/Aether/internal/mcpbridge"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// BinaryPath is the argv0-selected path for the coordination CLI. The
	// scheduler mounts the same digest-staged server binary here alongside
	// mcpbridge.BinaryPath, whose historical path remains the MCP/status hook
	// surface.
	BinaryPath = "/usr/local/bin/aether-internal"
	// SchemaVersion is the machine-readable CLI envelope version.
	SchemaVersion = protocol.CoordWireVersion

	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
	ExitDenied  = 3
	ExitMissing = 4
)

// Config makes Run testable without changing the command's wire contract.
// Socket is intentionally not a command-line option: production always uses
// the run-mounted v3 socket. Tests may point it at a temporary socket.
type Config struct {
	Socket string
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// Envelope is the stable output wrapper for every command except skill,
// whose output is concise human-readable instructions by design.
type Envelope struct {
	SchemaVersion string    `json:"schema_version"`
	OK            bool      `json:"ok"`
	Result        any       `json:"result,omitempty"`
	Error         *CLIError `json:"error,omitempty"`
}

// CLIError is the stable machine-readable error object.
type CLIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Main runs the command with process standard streams and returns its exit
// status. cmd/aether-server uses this when argv0 is aether-internal.
func Main(args []string) int {
	code, err := Run(context.Background(), args, Config{})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "aether-internal: %v\n", err)
	}
	return code
}

// Execute is a convenience for embedders and focused command tests.
func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) (int, error) {
	return Run(ctx, args, Config{In: in, Out: out, ErrOut: errOut})
}

// Run executes one command and writes exactly one JSON envelope for success or
// failure, except skill which writes its text directly. It never changes the
// calling process's cwd or environment and never writes a user repository.
func Run(ctx context.Context, args []string, cfg Config) (int, error) {
	if cfg.Socket == "" {
		cfg.Socket = mcpbridge.SocketPath
	}
	if cfg.In == nil {
		cfg.In = os.Stdin
	}
	if cfg.Out == nil {
		cfg.Out = os.Stdout
	}
	if cfg.ErrOut == nil {
		cfg.ErrOut = os.Stderr
	}
	if len(args) == 0 {
		return fail(cfg.Out, protocol.CodeInvalidRequest, "a command is required")
	}

	var result any
	var err error
	switch args[0] {
	case "status":
		result, err = status(ctx, cfg.Socket, args[1:])
	case "skill":
		return skill(ctx, cfg.Socket, args[1:], cfg.Out)
	case "send":
		result, err = send(ctx, cfg.Socket, args[1:], cfg.In, cfg.ErrOut)
	case "inbox":
		result, err = inbox(ctx, cfg.Socket, args[1:])
	case "ask":
		result, err = ask(ctx, cfg.Socket, args[1:], cfg.In, cfg.ErrOut)
	case "reply":
		result, err = reply(ctx, cfg.Socket, args[1:], cfg.In, cfg.ErrOut)
	case "report":
		result, err = report(ctx, cfg.Socket, args[1:], cfg.In, cfg.ErrOut)
	default:
		return fail(cfg.Out, protocol.CodeMethodNotFound, "unknown command: "+args[0])
	}
	if err != nil {
		return fail(cfg.Out, errorCode(err), err.Error())
	}
	if err := writeEnvelope(cfg.Out, Envelope{SchemaVersion: SchemaVersion, OK: true, Result: result}); err != nil {
		return ExitFailure, fmt.Errorf("write success response: %w", err)
	}
	return ExitOK, nil
}

func status(ctx context.Context, socket string, args []string) (protocol.CoordStatusResult, error) {
	fs := newFlags("status")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args); err != nil {
		return protocol.CoordStatusResult{}, err
	}
	if !*jsonOut || fs.NArg() != 0 {
		return protocol.CoordStatusResult{}, usageError("status requires --json")
	}
	var out protocol.CoordStatusResult
	if err := mcpbridge.Call(ctx, socket, protocol.MethodCoordStatus, nil, &out); err != nil {
		return out, err
	}
	if out.Peers == nil {
		out.Peers = []protocol.CoordPeer{}
	}
	if out.Capabilities == nil {
		out.Capabilities = []string{}
	}
	return out, nil
}

func skill(ctx context.Context, socket string, args []string, out io.Writer) (int, error) {
	fs := newFlags("skill")
	if err := parseFlags(fs, args); err != nil {
		return fail(out, protocol.CodeInvalidParams, err.Error())
	}
	if fs.NArg() != 0 {
		return fail(out, protocol.CodeInvalidParams, "skill takes no arguments")
	}
	var status protocol.CoordStatusResult
	if err := mcpbridge.Call(ctx, socket, protocol.MethodCoordStatus, nil, &status); err != nil {
		return fail(out, errorCode(err), err.Error())
	}
	if _, err := fmt.Fprintf(out, "Aether coordination skill %s\nRun: %s\n", SchemaVersion, status.RunID); err != nil {
		return ExitFailure, fmt.Errorf("write skill header: %w", err)
	}
	assignment := strings.TrimSpace(status.Task)
	if assignment == "" {
		assignment = "(assignment not supplied)"
	}
	if _, err := fmt.Fprintf(out, "Assignment: %s\n", assignment); err != nil {
		return ExitFailure, fmt.Errorf("write skill assignment: %w", err)
	}
	_, err := io.WriteString(out, "Inspect this assignment before acting. Stay within scope, use aether-internal to communicate with authorized peers, read inbox at natural checkpoints, ask questions when blocked, and report success, failure, or blocked with real evidence. Do not take new work after reporting.\n")
	if err != nil {
		return ExitFailure, fmt.Errorf("write skill workflow: %w", err)
	}
	return ExitOK, nil
}

func send(ctx context.Context, socket string, args []string, in io.Reader, errOut io.Writer) (protocol.CoordSendResult, error) {
	fs := newFlags("send")
	to := fs.String("to", "", "authorized peer run ID")
	body := fs.String("body", "", "message body")
	bodyFile := fs.String("body-file", "", "read message body from a file, or - for stdin")
	key := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.CoordSendResult{}, err
	}
	if *to == "" && fs.NArg() > 0 {
		*to, _ = positionalBody(fs.Args())
	}
	if *body == "" && fs.NArg() > 1 {
		_, *body = positionalBody(fs.Args())
	}
	text, err := resolveBody(*body, *bodyFile, in)
	if err != nil {
		return protocol.CoordSendResult{}, err
	}
	idempotencyKey, err := mutationKey("send", *key, errOut)
	if err != nil {
		return protocol.CoordSendResult{}, err
	}
	p := protocol.CoordSendParams{ToRunID: *to, Body: text, IdempotencyKey: idempotencyKey}
	var out protocol.CoordSendResult
	if err := mcpbridge.Call(ctx, socket, protocol.MethodCoordSend, p, &out); err != nil {
		return out, err
	}
	return out, nil
}

func inbox(ctx context.Context, socket string, args []string) (protocol.CoordInboxResult, error) {
	fs := newFlags("inbox")
	wait := fs.Int("wait", 0, "bounded wait in seconds")
	ack := fs.String("ack", "", "acknowledge the previous batch token")
	if err := parseFlags(fs, args); err != nil {
		return protocol.CoordInboxResult{}, err
	}
	if fs.NArg() != 0 {
		return protocol.CoordInboxResult{}, usageError("inbox takes --wait and --ack flags")
	}
	var out protocol.CoordInboxResult
	if err := mcpbridge.Call(ctx, socket, protocol.MethodCoordInbox, protocol.CoordInboxParams{AckToken: *ack, WaitSeconds: *wait}, &out); err != nil {
		return out, err
	}
	if out.Messages == nil {
		out.Messages = []protocol.CoordMessage{}
	}
	return out, nil
}

func ask(ctx context.Context, socket string, args []string, in io.Reader, errOut io.Writer) (protocol.CoordAskResult, error) {
	fs := newFlags("ask")
	to := fs.String("to", "", "authorized peer run ID")
	body := fs.String("body", "", "question body")
	bodyFile := fs.String("body-file", "", "read question from a file, or - for stdin")
	key := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.CoordAskResult{}, err
	}
	if *to == "" && fs.NArg() > 0 {
		*to, _ = positionalBody(fs.Args())
	}
	if *body == "" && fs.NArg() > 1 {
		_, *body = positionalBody(fs.Args())
	}
	text, err := resolveBody(*body, *bodyFile, in)
	if err != nil {
		return protocol.CoordAskResult{}, err
	}
	idempotencyKey, err := mutationKey("ask", *key, errOut)
	if err != nil {
		return protocol.CoordAskResult{}, err
	}
	p := protocol.CoordAskParams{ToRunID: *to, Body: text, IdempotencyKey: idempotencyKey}
	var out protocol.CoordAskResult
	if err := mcpbridge.Call(ctx, socket, protocol.MethodCoordAsk, p, &out); err != nil {
		return out, err
	}
	return out, nil
}

func reply(ctx context.Context, socket string, args []string, in io.Reader, errOut io.Writer) (protocol.CoordReplyResult, error) {
	fs := newFlags("reply")
	question := fs.String("question-id", "", "question ID to answer")
	body := fs.String("body", "", "reply body")
	bodyFile := fs.String("body-file", "", "read reply from a file, or - for stdin")
	key := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.CoordReplyResult{}, err
	}
	if *question == "" && fs.NArg() > 0 {
		*question, _ = positionalBody(fs.Args())
	}
	if *body == "" && fs.NArg() > 1 {
		_, *body = positionalBody(fs.Args())
	}
	text, err := resolveBody(*body, *bodyFile, in)
	if err != nil {
		return protocol.CoordReplyResult{}, err
	}
	idempotencyKey, err := mutationKey("reply", *key, errOut)
	if err != nil {
		return protocol.CoordReplyResult{}, err
	}
	p := protocol.CoordReplyParams{QuestionID: *question, Body: text, IdempotencyKey: idempotencyKey}
	var out protocol.CoordReplyResult
	if err := mcpbridge.Call(ctx, socket, protocol.MethodCoordReply, p, &out); err != nil {
		return out, err
	}
	return out, nil
}

func report(ctx context.Context, socket string, args []string, in io.Reader, errOut io.Writer) (protocol.CoordReportResult, error) {
	fs := newFlags("report")
	outcome := fs.String("outcome", "", "success, failure, or blocked")
	summary := fs.String("summary", "", "bounded outcome summary")
	summaryFile := fs.String("summary-file", "", "read summary from a file, or - for stdin")
	key := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	refs := stringList{}
	fs.Var(&refs, "evidence-ref", "evidence reference (repeatable)")
	if err := parseFlags(fs, args); err != nil {
		return protocol.CoordReportResult{}, err
	}
	if *summary == "" && fs.NArg() > 0 {
		*summary = strings.Join(fs.Args(), " ")
	}
	text, err := resolveBody(*summary, *summaryFile, in)
	if err != nil {
		return protocol.CoordReportResult{}, err
	}
	idempotencyKey, err := mutationKey("report", *key, errOut)
	if err != nil {
		return protocol.CoordReportResult{}, err
	}
	p := protocol.CoordReportParams{Outcome: *outcome, Summary: text, EvidenceRefs: append([]string(nil), refs...), IdempotencyKey: idempotencyKey}
	var out protocol.CoordReportResult
	if err := mcpbridge.Call(ctx, socket, protocol.MethodCoordReport, p, &out); err != nil {
		return out, err
	}
	return out, nil
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	if value == "" {
		return errors.New("evidence-ref cannot be empty")
	}
	*s = append(*s, value)
	return nil
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usageError(fs.Name() + ": help requested")
		}
		return usageError(err.Error())
	}
	return nil
}

func positionalBody(args []string) (string, string) {
	if len(args) == 0 {
		return "", ""
	}
	return args[0], strings.Join(args[1:], " ")
}

func resolveBody(body, file string, in io.Reader) (result string, retErr error) {
	if body != "" && file != "" {
		return "", usageError("choose one of --body and --body-file")
	}
	if file == "" && body != "-" {
		return body, nil
	}
	r := in
	var f *os.File
	if file != "-" {
		var err error
		f, err = os.Open(file)
		if err != nil {
			return "", fmt.Errorf("read body file: %w", err)
		}
		defer func() {
			if closeErr := f.Close(); closeErr != nil && retErr == nil {
				retErr = fmt.Errorf("close body file: %w", closeErr)
			}
		}()
		r = f
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(data), nil
}

var keyCounter atomic.Uint64

func mutationKey(prefix, explicit string, errOut io.Writer) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	var b [12]byte
	var key string
	if _, err := rand.Read(b[:]); err == nil {
		key = prefix + "-" + hex.EncodeToString(b[:])
	} else {
		key = fmt.Sprintf("%s-%d", prefix, keyCounter.Add(1))
	}
	if _, err := fmt.Fprintf(errOut, "idempotency-key: %s\n", key); err != nil {
		return "", fmt.Errorf("write idempotency key: %w", err)
	}
	return key, nil
}

func usageError(message string) error { return &CLIUsageError{Message: message} }

type CLIUsageError struct{ Message string }

func (e *CLIUsageError) Error() string { return e.Message }

func errorCode(err error) int {
	if err == nil {
		return 0
	}
	if _, ok := err.(*CLIUsageError); ok {
		return protocol.CodeInvalidParams
	}
	if code := mcpbridge.ErrorCode(err); code != 0 {
		return code
	}
	return protocol.CodeInternal
}

func exitFor(code int) int {
	switch code {
	case protocol.CodeParse, protocol.CodeInvalidRequest, protocol.CodeInvalidParams, protocol.CodeMethodNotFound:
		return ExitUsage
	case protocol.CodeDenied, protocol.CodeConflict, protocol.CodeInvalidState:
		return ExitDenied
	case protocol.CodeNotFound, protocol.CodeUnavailable:
		return ExitMissing
	default:
		return ExitFailure
	}
}

func fail(out io.Writer, code int, message string) (int, error) {
	if err := writeEnvelope(out, Envelope{SchemaVersion: SchemaVersion, OK: false, Error: &CLIError{Code: code, Message: message}}); err != nil {
		return ExitFailure, fmt.Errorf("write error response for %q: %w", message, err)
	}
	return exitFor(code), nil
}

func writeEnvelope(out io.Writer, envelope Envelope) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}
