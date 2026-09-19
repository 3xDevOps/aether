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

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// SchemaVersion is the machine-readable CLI envelope version.
	SchemaVersion = protocol.CoordWireVersion

	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
	ExitDenied  = 3
	ExitMissing = 4
)

var defaultSocketPath = coordtransport.SocketPath

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
		cfg.Socket = defaultSocketPath
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
		return fail(cfg.Out, protocol.CodeInvalidRequest, "a command is required; use --help for command usage")
	}
	if args[0] == "-h" || args[0] == "--help" {
		return writeHelp(cfg.Out, "")
	}
	if len(args) >= 2 && (args[len(args)-1] == "-h" || args[len(args)-1] == "--help") {
		return writeHelp(cfg.Out, strings.Join(args[:len(args)-1], " "))
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
	case "task", "worker":
		result, err = missionCommand(ctx, cfg.Socket, args[0], args[1:], cfg.In, cfg.ErrOut)
	case "integration":
		if len(args) < 2 {
			return fail(cfg.Out, protocol.CodeInvalidParams, "integration requires a subcommand")
		}
		result, err = integrationCommand(ctx, cfg.Socket, args[1], args[2:], cfg.In)
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

const topUsage = `usage: aether-internal <command> [options]

Commands:
  status    inspect this run and its authorized peers
  skill     print the coordination workflow and live assignment
  send      send a durable message to an authorized peer
  inbox     read the at-least-once inbox
  ask       ask an authorized peer a durable question
  reply     answer a durable question
  task      inspect and mutate mission task revisions
  worker    inspect and manage mission worker attempts
  integration run the five integrator candidate operations
  report    submit a durable outcome with evidence references

Run "aether-internal <command> --help" for command options.
`

var commandUsages = map[string]string{
	"status": `usage: aether-internal status --json

Print this run's identity, assignment, authorized peers, unread count, and capabilities.
`,
	"skill": `usage: aether-internal skill

Print the bounded coordination workflow. Outside a coordinated run this still
prints the general workflow, without claiming an identity or assignment.
`,
	"send": `usage: aether-internal send --to <run-id> (--body <text> | --body-file <path>) [--idempotency-key <key>]

Send one durable message. A body file of "-" reads standard input.
`,
	"inbox": `usage: aether-internal inbox [--wait <seconds>] [--ack <token>]

Read one bounded inbox batch. Supplying --ack acknowledges the previous batch.
`,
	"ask": `usage: aether-internal ask --to <run-id> (--body <text> | --body-file <path>) [--idempotency-key <key>]

Ask one durable, correlated question. A body file of "-" reads standard input.
`,
	"reply": `usage: aether-internal reply --question-id <id> (--body <text> | --body-file <path>) [--idempotency-key <key>]

Reply to the sender of one durable question. A body file of "-" reads standard input.
`,
	"task": `usage: aether-internal task <show|list|propose|revise|accept|accept-submission|abandon> [options]

Task mutations use the assignment and integrator authority on the run socket and require explicit idempotency keys.
Use --revision-file path or --revision-file - for bounded JSON revisions.
`,
	"worker": `usage: aether-internal worker <start|list|inspect|cancel|retry> [options]

Worker mutations use the mission authority on the run socket. Dispatch keys
are explicit identities for retry-safe starts and retries.
`,
	"report": `usage: aether-internal report --outcome <success|failure|blocked> (--summary <text> | --summary-file <path>) [--evidence-ref <ref>] [--idempotency-key <key>]

Submit one durable outcome. A summary file of "-" reads standard input.
`,
	"integration": `usage: aether-internal integration <prepare|show|verify|request-delivery|deliver> --params-file FILE|- [--json]

Agent integration is limited to these five assignment-scoped operations.
The mounted socket supplies caller identity. JSON input is limited to 32 KiB.
`,
	"integration prepare": `usage: aether-internal integration prepare --params-file FILE|- [--json]

Required JSON: target_ref, expected_target_revision, idempotency_key.
The server resolves omitted workspace_id, mission_id, and accepted submissions
from the current integrator assignment. Explicit submissions select exact
accepted inputs in the supplied order. Optional: required_sources.
Retry uncertain outcomes with the same parameters and idempotency_key.
`,
	"integration show": `usage: aether-internal integration show --params-file FILE|- [--json]

Required JSON: workspace_id, candidate_id.
Returns the exact candidate revision, verification results, and delivery state.
`,
	"integration verify": `usage: aether-internal integration verify --params-file FILE|- [--json]

Required JSON: workspace_id, candidate_id, candidate_revision, argv,
idempotency_key. Optional: timeout_seconds.
argv is a JSON string array. Poll show for the durable verification result.
`,
	"integration request-delivery": `usage: aether-internal integration request-delivery --params-file FILE|- [--json]

Required JSON: workspace_id, candidate_id, candidate_revision, verification_ids,
action ("update_ref" or "proposal"), idempotency_key.
This requests a human decision; it does not approve or deliver the candidate.
`,
	"integration deliver": `usage: aether-internal integration deliver --params-file FILE|- [--json]

Required JSON: workspace_id, candidate_id, request_id, request_version.
Use the human-approved request returned by show. Replay the same request after
an uncertain outcome; do not invent another delivery request.
`,
	"task show":              "usage: aether-internal task show --task-id <id>\n",
	"task list":              "usage: aether-internal task list --mission-id <id>\n",
	"task propose":           "usage: aether-internal task propose --mission-id <id> --idempotency-key <key> (--revision <json> | --revision-file <path>)\n",
	"task revise":            "usage: aether-internal task revise --task-id <id> --idempotency-key <key> (--revision <json> | --revision-file <path>)\n",
	"task accept":            "usage: aether-internal task accept --task-id <id> --revision <n> --expected-integrator-generation <n> --idempotency-key <key>\n",
	"task accept-submission": "usage: aether-internal task accept-submission --submission-id <id> --expected-integrator-generation <n> --expected-accepted-set-version <n> --idempotency-key <key> [--scope-disposition <reason>]\n",
	"task abandon":           "usage: aether-internal task abandon --task-id <id> --expected-integrator-generation <n> --idempotency-key <key>\n",
	"worker start":           "usage: aether-internal worker start --mission-id <id> --task-id <id> --task-revision <n> --dispatch-key <key> --harness <name> --mode <mode> --account-owner-id <id> --run-owner-id <id> --expected-integrator-generation <n>\n",
	"worker list":            "usage: aether-internal worker list --mission-id <id> [--task-id <id>]\n",
	"worker inspect":         "usage: aether-internal worker inspect --attempt-id <id>\n",
	"worker cancel":          "usage: aether-internal worker cancel --attempt-id <id> --expected-integrator-generation <n> --idempotency-key <key>\n",
	"worker retry":           "usage: aether-internal worker retry --attempt-id <id> --dispatch-key <key> --expected-integrator-generation <n>\n",
}

func writeHelp(out io.Writer, command string) (int, error) {
	text, ok := commandUsages[command]
	if command == "" {
		text, ok = topUsage, true
	}
	if !ok {
		return fail(out, protocol.CodeMethodNotFound, "unknown command: "+command)
	}
	if _, err := io.WriteString(out, text); err != nil {
		return ExitFailure, fmt.Errorf("write help: %w", err)
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
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordStatus, nil, &out); err != nil {
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
	if socket == "" {
		return writeSkill(out, nil)
	}
	var status protocol.CoordStatusResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordStatus, nil, &status); err != nil {
		if errorCode(err) == protocol.CodeUnavailable {
			return writeSkill(out, nil)
		}
		return fail(out, errorCode(err), err.Error())
	}
	return writeSkill(out, &status)
}

const skillWorkflow = `Workflow:
1. Inspect the assignment and acceptance requirements before acting.
2. Stay within the assigned scope; do not invent identity or authority.
3. Use aether-internal to read the inbox at natural checkpoints and ask authorized peers when blocked.
4. Keep evidence for the work you perform and report success, failure, or blocked.
5. Read the inbox once more before reporting, then take no new work after submission.
`
const integratorWorkflow = `Integrator candidate flow:
Create JSON parameter files outside read-only /run/aether, or use
--params-file - to read JSON from stdin. Each command's --help lists its fields.
  Prepare: aether-internal integration prepare --params-file /tmp/aether-prepare.json --json
  Review:  aether-internal integration show --params-file /tmp/aether-show.json --json
  Verify:  aether-internal integration verify --params-file /tmp/aether-verify.json --json
  Request: aether-internal integration request-delivery --params-file /tmp/aether-request-delivery.json --json
The request-delivery result is a human-decision boundary. Do not approve a
delivery from the agent socket; a human reviews and decides through the
authenticated review surface. Only after approval:
  Deliver: aether-internal integration deliver --params-file /tmp/aether-deliver.json --json
Recovery: if a call reports unavailable after it may have committed, retry the
same params file with the same idempotency_key; never invent a replacement key.
`

func writeSkill(out io.Writer, status *protocol.CoordStatusResult) (int, error) {
	if _, err := fmt.Fprintf(out, "Aether coordination skill %s\n", SchemaVersion); err != nil {
		return ExitFailure, fmt.Errorf("write skill header: %w", err)
	}
	if status == nil {
		if _, err := io.WriteString(out, "No coordination socket is mounted; this is the general workflow and carries no run identity or assignment.\n"); err != nil {
			return ExitFailure, fmt.Errorf("write skill availability: %w", err)
		}
	} else {
		assignment := strings.TrimSpace(status.Task)
		if assignment == "" {
			assignment = "(assignment not supplied)"
		}
		if len(assignment) > protocol.CoordMaxStatusTaskBytes {
			assignment = assignment[:protocol.CoordMaxStatusTaskBytes] + "…"
		}
		if _, err := fmt.Fprintf(out, "Run: %s\nAssignment: %s\n", boundedSkillField(status.RunID), assignment); err != nil {
			return ExitFailure, fmt.Errorf("write skill assignment: %w", err)
		}
		integratorRole := false
		if assignment := status.Assignment; assignment != nil {
			if _, err := fmt.Fprintf(out, "Mission: %s\nRole: %s\nTask ID: %s\nTask revision: %d\nAttempt ID: %s\nIntegrator run: %s\nIntegrator generation: %d\n",
				boundedSkillField(assignment.MissionID),
				boundedSkillField(assignment.Role),
				boundedSkillField(assignment.TaskID),
				assignment.TaskRevision,
				boundedSkillField(assignment.AttemptID),
				boundedSkillField(assignment.IntegratorRunID),
				assignment.IntegratorGeneration); err != nil {
				return ExitFailure, fmt.Errorf("write skill mission assignment: %w", err)
			}
			switch assignment.Role {
			case "integrator":
				integratorRole = true
				if len(assignment.ExecutionChoices) > 0 {
					if _, err := fmt.Fprintf(out, "Approved execution choices: %s\n", boundedSkillExecutionChoices(assignment.ExecutionChoices)); err != nil {
						return ExitFailure, fmt.Errorf("write skill execution choices: %w", err)
					}
				}
				if _, err := fmt.Fprintf(out, "Attempt allowance: active=%d/%d total=%d/%d remaining_concurrent=%d remaining_total=%d\n",
					assignment.ActiveAttempts,
					assignment.MaxConcurrentAttempts,
					assignment.TotalAttempts,
					assignment.MaxTotalAttempts,
					assignment.MaxConcurrentAttempts-assignment.ActiveAttempts,
					assignment.MaxTotalAttempts-assignment.TotalAttempts); err != nil {
					return ExitFailure, fmt.Errorf("write skill attempt allowance: %w", err)
				}
				if _, err := io.WriteString(out, integratorWorkflow); err != nil {
					return ExitFailure, fmt.Errorf("write skill integration workflow: %w", err)
				}
			case "worker":
				if _, err := io.WriteString(out, "Worker scope: read and propose changes only for the assigned task; do not spawn workers.\n"); err != nil {
					return ExitFailure, fmt.Errorf("write skill worker scope: %w", err)
				}
			}
			capabilities := skillCapabilitiesForRole(assignment.Capabilities, integratorRole)
			if len(capabilities) > 0 {
				bounded := boundedSkillCapabilities(capabilities)
				if _, err := fmt.Fprintf(out, "Assignment capabilities: %s\n", bounded); err != nil {
					return ExitFailure, fmt.Errorf("write skill assignment capabilities: %w", err)
				}
			}
		}
		capabilities := skillCapabilitiesForRole(status.Capabilities, integratorRole)
		if len(capabilities) > 0 {
			bounded := boundedSkillCapabilities(capabilities)
			if _, err := fmt.Fprintf(out, "Capabilities: %s\n", bounded); err != nil {
				return ExitFailure, fmt.Errorf("write skill capabilities: %w", err)
			}
		}
	}
	if _, err := io.WriteString(out, skillWorkflow); err != nil {
		return ExitFailure, fmt.Errorf("write skill workflow: %w", err)
	}
	return ExitOK, nil
}

func boundedSkillField(value string) string {
	if len(value) <= 256 {
		return value
	}
	return value[:256] + "…"
}
func skillCapabilitiesForRole(values []string, integrator bool) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.HasPrefix(value, "integration.") {
			if !integrator || !skillIntegrationCapability(value) {
				continue
			}
		}
		out = append(out, value)
	}
	return out
}

func skillIntegrationCapability(value string) bool {
	switch value {
	case protocol.MethodIntegrationPrepare,
		protocol.MethodIntegrationShow,
		protocol.MethodIntegrationVerify,
		protocol.MethodIntegrationRequestDelivery,
		protocol.MethodIntegrationDeliver:
		return true
	default:
		return false
	}
}

func boundedSkillExecutionChoices(values []protocol.MissionExecutionChoice) string {
	var b strings.Builder
	for _, choice := range values {
		value := "account=" + boundedSkillField(choice.AccountMemberID) +
			" harness=" + boundedSkillField(choice.Harness) +
			" mode=" + boundedSkillField(choice.Mode)
		separator := 0
		if b.Len() > 0 {
			separator = 2
		}
		if b.Len()+separator+len(value) > 2048 {
			break
		}
		if separator > 0 {
			b.WriteString("; ")
		}
		b.WriteString(value)
	}
	if b.Len() == 2048 {
		return b.String() + "…"
	}
	return b.String()
}

func boundedSkillCapabilities(values []string) string {
	var b strings.Builder
	for _, value := range values {
		value = boundedSkillField(value)
		if b.Len() > 0 {
			if b.Len()+2+len(value) > 2048 {
				break
			}
			b.WriteString(", ")
		} else if len(value) > 2048 {
			value = value[:2048]
		}
		b.WriteString(value)
	}
	if b.Len() == 2048 {
		return b.String() + "…"
	}
	return b.String()
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
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordSend, p, &out); err != nil {
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
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordInbox, protocol.CoordInboxParams{AckToken: *ack, WaitSeconds: *wait}, &out); err != nil {
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
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordAsk, p, &out); err != nil {
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
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordReply, p, &out); err != nil {
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
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordReport, p, &out); err != nil {
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
		if len(body) > protocol.CoordMaxBodyBytes {
			return "", usageError(fmt.Sprintf("body exceeds %d bytes", protocol.CoordMaxBodyBytes))
		}
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
	data, err := io.ReadAll(io.LimitReader(r, protocol.CoordMaxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if len(data) > protocol.CoordMaxBodyBytes {
		return "", usageError(fmt.Sprintf("body exceeds %d bytes", protocol.CoordMaxBodyBytes))
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
	if code := coordtransport.ErrorCode(err); code != 0 {
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
