package coordcli

import (
	"context"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// planCommand routes the mission question, plan, and start group.
func planCommand(ctx context.Context, socket string, args []string, in io.Reader) (any, error) {
	if len(args) == 0 {
		return nil, usageError("mission requires a subcommand: question, plan, or start")
	}
	switch args[0] {
	case "question":
		if len(args) < 2 {
			return nil, usageError("mission question requires a subcommand: ask")
		}
		switch args[1] {
		case "ask":
			return missionQuestionAsk(ctx, socket, args[2:], in)
		default:
			return nil, usageError("unknown mission question command: " + args[1])
		}
	case "plan":
		if len(args) < 2 {
			return nil, usageError("mission plan requires a subcommand: show")
		}
		switch args[1] {
		case "show":
			return missionPlanShow(ctx, socket, args[2:])
		default:
			return nil, usageError("unknown mission plan command: " + args[1])
		}
	case "start":
		return missionStart(ctx, socket, args[1:])
	default:
		return nil, usageError("unknown mission command: " + args[0])
	}
}

func missionStart(ctx context.Context, socket string, args []string) (protocol.MissionStartResult, error) {
	fs := newFlags("mission start")
	missionID := fs.String("mission-id", "", "mission ID")
	key := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.MissionStartResult{}, err
	}
	if *missionID == "" || *key == "" || fs.NArg() != 0 {
		return protocol.MissionStartResult{}, usageError("mission start requires --mission-id and --idempotency-key")
	}
	p := protocol.MissionStartParams{MissionID: *missionID, IdempotencyKey: *key}
	var out protocol.MissionStartResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMissionStart, p, &out); err != nil {
		return out, err
	}
	return out, nil
}

func missionQuestionAsk(ctx context.Context, socket string, args []string, in io.Reader) (protocol.MissionQuestionResult, error) {
	fs := newFlags("mission question ask")
	body := fs.String("body", "", "question for the accountable human")
	bodyFile := fs.String("body-file", "", "read the question from a file, or - for stdin")
	key := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.MissionQuestionResult{}, err
	}
	if (*body == "" && *bodyFile == "") || *key == "" || fs.NArg() != 0 {
		return protocol.MissionQuestionResult{}, usageError("mission question ask requires --body or --body-file and --idempotency-key")
	}
	text, err := resolveBody(*body, *bodyFile, in)
	if err != nil {
		return protocol.MissionQuestionResult{}, err
	}
	p := protocol.MissionQuestionAskParams{Body: text, IdempotencyKey: *key}
	var out protocol.MissionQuestionResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMissionQuestionAsk, p, &out); err != nil {
		return out, err
	}
	return out, nil
}

func missionPlanShow(ctx context.Context, socket string, args []string) (protocol.MissionPlanShowResult, error) {
	fs := newFlags("mission plan show")
	wait := fs.Int("wait", 0, "bounded wait in seconds")
	if err := parseFlags(fs, args); err != nil {
		return protocol.MissionPlanShowResult{}, err
	}
	if fs.NArg() != 0 {
		return protocol.MissionPlanShowResult{}, usageError("mission plan show takes only --wait")
	}
	if *wait < 0 || *wait > protocol.CoordMaxInboxWaitSeconds {
		return protocol.MissionPlanShowResult{}, usageError(fmt.Sprintf("mission plan show: --wait must be between 0 and %d", protocol.CoordMaxInboxWaitSeconds))
	}
	var out protocol.MissionPlanShowResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{WaitSeconds: *wait}, &out); err != nil {
		return out, err
	}
	if out.Questions == nil {
		out.Questions = []protocol.MissionQuestion{}
	}
	return out, nil
}

const integratorRole = "You are this mission's integrator: turn the objective into tasks for workers and coordinate them; do not implement the objective yourself. Workers reach you with send and ask, and every worker report arrives in your inbox; answer questions with reply.\n"

const planningFlow = `Next: plan the mission, then start it. No human approves the plan.
Ask the accountable human only if the objective is genuinely ambiguous:
  aether-internal mission question ask --help
  aether-internal mission plan show --wait 30
Otherwise go straight to proposing tasks. Discover the flags and revision JSON:
  aether-internal task propose --help
  aether-internal task revise --help
Declare scope.expected_paths for every task and retain its exclusions.
When every question is answered and the tasks are proposed, start the mission:
  aether-internal mission start --help
Starting accepts every proposed task and makes the mission active. Then rerun
aether-internal skill. Do not report while planning.
`

const activeFlow = `Next: dispatch accepted tasks and review their submissions.
  aether-internal worker start --help
  aether-internal task accept-submission --help
Each worker report arrives in your inbox as a message from the worker run
whose correlation_id is the report ID; worker list shows its outcome. While
waiting for workers, wait with aether-internal inbox --wait 30 instead of
polling worker list. A worker that exits without reporting sends nothing.
Use current task revisions, integrator generation, accepted-set version, and
execution choices from live results; never guess IDs or generations.
New or revised work needs no approval: propose or revise it, accept it with
task accept, then dispatch it. Cancel or wait for a worker before revising its task.
  aether-internal task accept --help
  aether-internal task abandon --help
Never wait for a human. Once the combined result is verified and delivered,
or the objective has nothing to deliver and its findings are gathered, report
success; that completes the mission and stops leftover workers. Success is
refused while an approved delivery request has not run; deliver it first.
`

const completedFlow = `The mission is completed. For follow-up work, propose a task or start a
worker: either makes the mission active again. Then rerun aether-internal skill.
  aether-internal task propose --help
  aether-internal worker start --help
Otherwise take no further action.
`

const cancelledFlow = `The mission was cancelled and its runs are being stopped.
Do not start work, mutate tasks, or report an outcome.
`

// writeSkillPhase prints only the immediate instructions for the current phase.
func writeSkillPhase(out io.Writer, assignment *protocol.CoordMissionAssignment) error {
	if _, err := fmt.Fprintf(out, "Phase: %s\nOpen questions: %d\n",
		boundedSkillField(assignment.Phase), assignment.OpenQuestions); err != nil {
		return fmt.Errorf("write skill phase: %w", err)
	}
	var flow string
	switch assignment.Phase {
	case "planning":
		flow = planningFlow
	case "active":
		flow = activeFlow
	case "completed":
		flow = completedFlow
	case "cancelled":
		flow = cancelledFlow
	default:
		flow = "No recognized mission phase supplied. Refresh aether-internal status before mission actions.\n"
	}
	if _, err := io.WriteString(out, flow); err != nil {
		return fmt.Errorf("write skill phase guidance: %w", err)
	}
	return nil
}
