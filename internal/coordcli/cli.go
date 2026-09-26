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
	"github.com/3xDevOps/Aether/internal/shellquote"
	"github.com/3xDevOps/Aether/internal/version"
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

// Envelope is the stable output wrapper for state commands. Skill and help
// write text; hook writes the harness's native response.
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

// Run executes one command without changing the calling process's cwd,
// environment, or repository. State commands write one JSON envelope; skill,
// help, and hook use their documented output formats.
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
	case "hook":
		return hook(ctx, cfg, args[1:])
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
	case "mission":
		result, err = planCommand(ctx, cfg.Socket, args[1:], cfg.In)
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
  hook      run a native inbox hook or print a copyable integration file
  send      send a durable message to an authorized peer
  inbox     read the at-least-once inbox
  ask       ask an authorized peer a durable question
  reply     answer a durable question
  mission   ask the accountable human and submit the plan for review
  task      inspect and mutate mission task revisions
  worker    inspect and manage mission worker attempts
  integration run the five integrator candidate operations
  report    submit a durable outcome with evidence references

Run "aether-internal <command> --help" for command options.
`

var commandUsages = map[string]string{
	"hook": hookUsage,
	"status": `usage: aether-internal status [--json]

Print this run's identity, assignment, authorized peers, unread count, and capabilities
in the v3 JSON envelope. --json is optional; output is always JSON.
`,
	"skill": `usage: aether-internal skill

Print the coordination workflow, live assignment, and read-only hook installation
checks. Missing integrations include commands to obtain copyable hook files.
Outside a coordinated run this prints general guidance without claiming identity.
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
	"mission": `usage: aether-internal mission <clarification|question|plan> <subcommand> [options]

mission question ask asks the accountable human, who answers in the dashboard.
ask --to <run-id> asks a peer agent run, which answers with reply. They are
separate mailboxes. The mission is the run's own; no command takes a mission ID.
`,
	"mission clarification": `usage: aether-internal mission clarification complete --idempotency-key <key>

Declare that clarification is done and the plan can be written. Only the
integrator may complete it, and only while the mission is in the planning
phase.
`,
	"mission clarification complete": `usage: aether-internal mission clarification complete --idempotency-key <key>

Move the mission from planning to clarified. Questions are optional, but the
call is refused while a question you asked is unanswered.
`,
	"mission question": `usage: aether-internal mission question ask (--body <text> | --body-file <path>) --idempotency-key <key>

mission question ask asks the accountable human, who answers in the dashboard.
ask --to <run-id> asks a peer agent run, which answers with reply. They are
separate mailboxes.
`,
	"mission question ask": `usage: aether-internal mission question ask (--body <text> | --body-file <path>) --idempotency-key <key>

Ask the accountable human one clarifying question. A body file of "-" reads
standard input. Only the integrator may ask, in the planning and clarified
phases; asking in clarified returns the mission to planning until the question
is answered.
`,
	"mission plan": `usage: aether-internal mission plan <show|submit> [options]

show reads the gate state, questions, and review rounds. submit sends the
proposed tasks to the accountable human for a decision.
`,
	"mission plan show": `usage: aether-internal mission plan show [--wait <seconds>]

Read the mission phase, plan version, open questions, and review rounds.
--wait asks the server to wait up to 30 seconds for a change; it is not a
client polling loop.
`,
	"mission plan submit": `usage: aether-internal mission plan submit (--summary <text> | --summary-file <path>) --idempotency-key <key>

Submit the pending tasks and revisions as a plan for human review. For an
initial plan, clarification must be complete first; from the active phase this
submits an amendment to the approved plan. A summary file of "-" reads standard
input.
`,
	"task": `usage: aether-internal task <show|list|propose|revise|accept|accept-submission|abandon> [options]

Task commands use the live assignment. Workers may read and propose within
their scope; accepting or abandoning tasks is integrator-only. Mutations
require explicit idempotency keys.
Use task propose --help or task revise --help for revision JSON and required flags.
`,
	"worker": `usage: aether-internal worker <start|list|inspect|cancel|retry> [options]

Worker mutations use the mission authority on the run socket. Dispatch keys
are explicit identities for retry-safe starts and retries.
`,
	"report": `usage: aether-internal report --outcome <success|failure|blocked> (--summary <text> | --summary-file <path>) [--evidence-ref <ref>] [--idempotency-key <key>]

Submit one durable outcome. Success/failure are terminal worker outcomes;
blocked is a nonterminal observation, not a way to wait for a peer or human.
A summary file of "-" reads standard input.
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
	"task propose":           "usage: aether-internal task propose --mission-id <id> --idempotency-key <key> (--revision <json> | --revision-file <path>)\n" + taskRevisionHelp,
	"task revise":            "usage: aether-internal task revise --task-id <id> --idempotency-key <key> (--revision <json> | --revision-file <path>)\n" + taskRevisionHelp,
	"task accept":            "usage: aether-internal task accept --task-id <id> --revision <n> --expected-integrator-generation <n> --idempotency-key <key>\n",
	"task accept-submission": "usage: aether-internal task accept-submission --submission-id <id> --expected-integrator-generation <n> --expected-accepted-set-version <n> --idempotency-key <key> [--scope-disposition <reason>]\n",
	"task abandon":           "usage: aether-internal task abandon --task-id <id> [--revision <n>] --expected-integrator-generation <n> --idempotency-key <key>\n\nWithout --revision the whole task is abandoned; a revision drops only that pending revision.\n",
	"worker start":           "usage: aether-internal worker start --mission-id <id> --task-id <id> --task-revision <n> --dispatch-key <key> --harness <name> --mode <mode> --account-owner-id <id> --run-owner-id <id> --expected-integrator-generation <n>\n",
	"worker list":            "usage: aether-internal worker list --mission-id <id> [--task-id <id>]\n",
	"worker inspect":         "usage: aether-internal worker inspect --attempt-id <id>\n",
	"worker cancel":          "usage: aether-internal worker cancel --attempt-id <id> --expected-integrator-generation <n> --idempotency-key <key>\n",
	"worker retry":           "usage: aether-internal worker retry --attempt-id <id> --dispatch-key <key> --expected-integrator-generation <n>\n",
}

// The input fields below are the author-supplied subset of protocol.TaskRevision.
const taskRevisionHelp = `
Revision JSON (maximum 32 KiB); title and objective are required non-empty strings:
  {"title":"Fix checkout","objective":"Reject expired sessions"}
Declare the intended scope before human approval, for example:
  {"title":"Fix checkout","objective":"Reject expired sessions","scope":{"expected_paths":["internal/checkout/"],"exclusions":["internal/checkout/generated/"]},"evidence_requirements":[{"kind":"transcript","detail":"Retain test output showing expired sessions are rejected"}]}
scope.expected_paths and scope.exclusions are arrays of repository-relative paths.
evidence_requirements is an array of {kind, detail} objects; detail is optional.
Kinds name retained evidence sources, such as transcript or git, not test types.
depends_on is an array of task IDs in this mission; the task stays blocked, and
worker start is refused, until each one's current revision has an accepted
submission. A cycle or an unknown, abandoned, or self ID is refused.
Set "material":true for changed scope, constraints, or success criteria requiring
a human-approved amendment. IDs, revision numbers, status, and timestamps are
server-managed; do not copy them from task show. Revise supplies the whole spec,
not a patch, so a revision without depends_on drops earlier dependencies.
--revision-file - reads stdin. Store files outside /run/aether.
`

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
	fs.Bool("json", false, "machine-readable output (always enabled)")
	if err := parseFlags(fs, args); err != nil {
		return protocol.CoordStatusResult{}, err
	}
	if fs.NArg() != 0 {
		return protocol.CoordStatusResult{}, usageError("status takes no positional arguments")
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

const skillBootstrap = `Inspect current state and messages:
  aether-internal status
  aether-internal inbox
  aether-internal --help
Status is a bounded summary, not the full task. Its capabilities describe
current authority; help describes syntax, not permission.
`

const skillWorkflow = `Coordination and completion:
Stay within your assignment. Peers listed by status are reachable with send,
ask, and reply; ask when a decision is theirs:
  aether-internal ask --help
Native hooks announce pending inbox items at harness lifecycle boundaries.
Check the inbox before waiting or reporting. Wait without reporting an outcome:
  aether-internal inbox --wait 30
Process the batch before acknowledging it: on the next inbox call pass
--ack with that batch's ack_token. Without acknowledgement it may repeat.
Read the inbox once more before a terminal report:
  aether-internal report --help
Success and failure are terminal worker outcomes: success submits the attempt
and stops the worker; failure ends it without a task result. Report success
only after finishing with required evidence, failure only if irrecoverable.
Blocked is a nonterminal durable observation, not a submission or a way to wait.
Do not report while idle or waiting on a peer or human. After a terminal
report, take no new work.
For an uncertain mutation, retry identical inputs with the same idempotency
key. Save the generated key printed to stderr if you omitted --idempotency-key.
Use a new key only for a new operation; receipt means durable storage, not read.
`

const integratorWorkflow = `When accepted submissions are ready for combined verification:
  aether-internal integration --help
  aether-internal integration prepare --help
Prepare, show, verify, request-delivery, then deliver only after human approval.
Each subcommand's --help lists its JSON fields; use --params-file with a file
outside /run/aether or "-" for stdin. The agent cannot approve delivery or
take over an integrator. A human uses Replace integrator if this run stops.
`

func writeSkill(out io.Writer, status *protocol.CoordStatusResult) (int, error) {
	if _, err := fmt.Fprintf(out, "Aether coordination skill %s\nCLI build: %s\n", SchemaVersion, version.String()); err != nil {
		return ExitFailure, fmt.Errorf("write skill header: %w", err)
	}
	if status == nil {
		if _, err := io.WriteString(out, "Role: unassigned (no coordination socket)\nNo live run identity or assignment; no mission authority. State commands need the mounted socket.\n"); err != nil {
			return ExitFailure, fmt.Errorf("write skill availability: %w", err)
		}
	} else {
		assignment := status.Assignment
		role := "ordinary"
		if assignment != nil {
			role = assignment.Role
		}
		if _, err := fmt.Fprintf(out, "Role: %s\nRun: %s\n", boundedSkillField(role), boundedSkillField(status.RunID)); err != nil {
			return ExitFailure, fmt.Errorf("write skill identity: %w", err)
		}
		if assignment == nil {
			if _, err := io.WriteString(out, "No mission authority; coordinate only with peers authorized by status.\n"); err != nil {
				return ExitFailure, fmt.Errorf("write skill ordinary scope: %w", err)
			}
		} else {
			if _, err := fmt.Fprintf(out, "Mission: %s\nIntegrator run: %s\nIntegrator generation: %d\n",
				boundedSkillField(assignment.MissionID), boundedSkillField(assignment.IntegratorRunID), assignment.IntegratorGeneration); err != nil {
				return ExitFailure, fmt.Errorf("write skill mission assignment: %w", err)
			}
			switch role {
			case "worker":
				if _, err := fmt.Fprintf(out, "Task ID: %s\nTask revision: %d\nAttempt ID: %s\nPhase: %s\nRead your full assigned task before acting (use the assigned revision above):\n  aether-internal task show --task-id %s\nWorkers may read and propose only; do not spawn workers, accept tasks, or perform mission/integration operations.\nCheck the inbox after reading the task, before each commit, and before reporting; sibling workers are listed by status.\n",
					boundedSkillField(assignment.TaskID), assignment.TaskRevision, boundedSkillField(assignment.AttemptID), boundedSkillField(assignment.Phase), shellquote.Quote(assignment.TaskID)); err != nil {
					return ExitFailure, fmt.Errorf("write skill worker scope: %w", err)
				}
			case "integrator":
				if _, err := io.WriteString(out, integratorRole); err != nil {
					return ExitFailure, fmt.Errorf("write skill integrator role: %w", err)
				}
				if err := writeSkillPhase(out, assignment); err != nil {
					return ExitFailure, err
				}
				if _, err := fmt.Fprintf(out, "Inspect this mission and discover command syntax:\n  aether-internal task list --mission-id %s\n  aether-internal worker list --mission-id %s\n  aether-internal task --help\n  aether-internal worker --help\n",
					shellquote.Quote(assignment.MissionID), shellquote.Quote(assignment.MissionID)); err != nil {
					return ExitFailure, fmt.Errorf("write skill mission commands: %w", err)
				}
				if len(assignment.ExecutionChoices) > 0 {
					if _, err := fmt.Fprintf(out, "Approved execution choices: %s\n", boundedSkillExecutionChoices(assignment.ExecutionChoices)); err != nil {
						return ExitFailure, fmt.Errorf("write skill execution choices: %w", err)
					}
				}
				if _, err := fmt.Fprintf(out, "Attempt allowance: active=%d/%d total=%d/%d remaining_concurrent=%d remaining_total=%d\n",
					assignment.ActiveAttempts, assignment.MaxConcurrentAttempts, assignment.TotalAttempts, assignment.MaxTotalAttempts,
					assignment.MaxConcurrentAttempts-assignment.ActiveAttempts, assignment.MaxTotalAttempts-assignment.TotalAttempts); err != nil {
					return ExitFailure, fmt.Errorf("write skill attempt allowance: %w", err)
				}
				if assignment.Phase == "active" || assignment.Phase == "amendment_review" {
					if _, err := io.WriteString(out, integratorWorkflow); err != nil {
						return ExitFailure, fmt.Errorf("write skill integration workflow: %w", err)
					}
				}
			}
		}
		summary := strings.TrimSpace(status.Task)
		if len(summary) > protocol.CoordMaxStatusTaskBytes {
			summary = summary[:protocol.CoordMaxStatusTaskBytes] + "…"
		}
		if _, err := fmt.Fprintf(out, "Task summary (bounded): %s\n", summary); err != nil {
			return ExitFailure, fmt.Errorf("write skill task summary: %w", err)
		}
	}
	if _, err := io.WriteString(out, skillBootstrap+skillWorkflow); err != nil {
		return ExitFailure, fmt.Errorf("write skill workflow: %w", err)
	}
	if err := writeHookInstallation(out); err != nil {
		return ExitFailure, fmt.Errorf("write hook installation guidance: %w", err)
	}
	return ExitOK, nil
}

func boundedSkillField(value string) string {
	if len(value) <= 256 {
		return value
	}
	return value[:256] + "…"
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
