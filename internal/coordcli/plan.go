package coordcli

import (
	"context"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// planCommand routes the two-level mission question and plan group. The
// mission is the caller's own, resolved from the run socket, so none of these
// commands takes a mission ID.
func planCommand(ctx context.Context, socket string, args []string, in io.Reader) (any, error) {
	if len(args) == 0 {
		return nil, usageError("mission requires a subcommand: question or plan")
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
			return nil, usageError("mission plan requires a subcommand: show or submit")
		}
		switch args[1] {
		case "show":
			return missionPlanShow(ctx, socket, args[2:])
		case "submit":
			return missionPlanSubmit(ctx, socket, args[2:], in)
		default:
			return nil, usageError("unknown mission plan command: " + args[1])
		}
	default:
		return nil, usageError("unknown mission command: " + args[0])
	}
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
	if out.PlanReviews == nil {
		out.PlanReviews = []protocol.MissionPlanReview{}
	}
	return out, nil
}

func missionPlanSubmit(ctx context.Context, socket string, args []string, in io.Reader) (protocol.MissionPlanSubmitResult, error) {
	fs := newFlags("mission plan submit")
	summary := fs.String("summary", "", "what will be built and why")
	summaryFile := fs.String("summary-file", "", "read the summary from a file, or - for stdin")
	key := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.MissionPlanSubmitResult{}, err
	}
	if (*summary == "" && *summaryFile == "") || *key == "" || fs.NArg() != 0 {
		return protocol.MissionPlanSubmitResult{}, usageError("mission plan submit requires --summary or --summary-file and --idempotency-key")
	}
	text, err := resolveBody(*summary, *summaryFile, in)
	if err != nil {
		return protocol.MissionPlanSubmitResult{}, err
	}
	p := protocol.MissionPlanSubmitParams{Summary: text, IdempotencyKey: *key}
	var out protocol.MissionPlanSubmitResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMissionPlanSubmit, p, &out); err != nil {
		return out, err
	}
	return out, nil
}

const planningFlow = `Planning flow:
  1. Ask the accountable human at least one question. The objective is short
     on purpose; name the decisions you would otherwise guess. This asks the
     human, not a peer run:
       aether-internal mission question ask --body 'question' --idempotency-key <key>
  2. Wait for answers. The call returns as soon as an answer or decision
     lands, and also returns unchanged after the wait elapses:
       aether-internal mission plan show --wait 30
     Repeat it while open_questions is above zero. Waiting is not being
     blocked: do not report an outcome.
  3. Check what is already proposed, then propose or revise:
       aether-internal task list --mission-id <mission-id>
       aether-internal task propose --mission-id <mission-id> \
         --idempotency-key <key> --revision-file /tmp/aether-task.json
     Revise an existing task rather than proposing a duplicate: after a
     revise decision the tasks you proposed last round are still there.
  4. Submit the plan for human review. You cannot submit while a question is
     unanswered:
       aether-internal mission plan submit --summary 'what will be built and why' --idempotency-key <key>
No worker can start until a human approves the plan.
`

const planReviewWait = `A human is reviewing the plan. Do not propose, revise, abandon, or accept
any task, and do not start a worker: the server refuses all of them in this
phase. Wait for the decision by repeating this call until the phase changes:
  aether-internal mission plan show --wait 30
Waiting for a human is not being blocked and is not an outcome; do not run
aether-internal report. When the phase changes, run aether-internal skill
again and follow the text for the new phase. If this run is stopped while
waiting, a human relaunches the integrator with Replace integrator.
`

const planRejected = `A human rejected the plan and this run is being cancelled. Do not start work,
do not change tasks, and do not report an outcome: the cancellation is the
terminal event.
`

// writeSkillPhase prints the plan-gate text for the integrator's own mission
// phase. An assignment that carries no phase predates the gate, so it gets the
// candidate flow alone rather than a claimed phase.
func writeSkillPhase(out io.Writer, assignment *protocol.CoordMissionAssignment) error {
	switch assignment.Phase {
	case "planning":
		if _, err := fmt.Fprintf(out, "Phase: planning\nPlan version: %d\nOpen questions: %d\n", assignment.PlanVersion, assignment.OpenQuestions); err != nil {
			return fmt.Errorf("write skill phase: %w", err)
		}
		if err := writeSkillFeedback(out, assignment.LatestFeedback); err != nil {
			return err
		}
		if _, err := io.WriteString(out, planningFlow); err != nil {
			return fmt.Errorf("write skill planning flow: %w", err)
		}
	case "plan_review":
		if _, err := fmt.Fprintf(out, "Phase: plan_review\nPlan version: %d\n", assignment.PlanVersion); err != nil {
			return fmt.Errorf("write skill phase: %w", err)
		}
		if err := writeSkillFeedback(out, assignment.LatestFeedback); err != nil {
			return err
		}
		if _, err := io.WriteString(out, planReviewWait); err != nil {
			return fmt.Errorf("write skill plan review wait: %w", err)
		}
	case "rejected":
		if _, err := io.WriteString(out, "Phase: rejected\n"+planRejected); err != nil {
			return fmt.Errorf("write skill phase: %w", err)
		}
	case "active":
		if _, err := fmt.Fprintf(out, "Phase: active\nPlan version: %d (approved)\n", assignment.PlanVersion); err != nil {
			return fmt.Errorf("write skill phase: %w", err)
		}
		fallthrough
	default:
		if _, err := io.WriteString(out, integratorWorkflow); err != nil {
			return fmt.Errorf("write skill integration workflow: %w", err)
		}
	}
	return nil
}

func writeSkillFeedback(out io.Writer, feedback string) error {
	if feedback == "" {
		return nil
	}
	// Feedback is the human's change request, already capped at 4 KiB by the
	// server; the identifier bound would cut it mid-sentence.
	if _, err := fmt.Fprintf(out, "Latest feedback: %s\n", feedback); err != nil {
		return fmt.Errorf("write skill latest feedback: %w", err)
	}
	return nil
}
