package coordcli

import (
	"context"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// planCommand routes the two-level mission clarification, question, and plan
// group. The mission is the caller's own, resolved from the run socket, so
// none of these commands takes a mission ID.
func planCommand(ctx context.Context, socket string, args []string, in io.Reader) (any, error) {
	if len(args) == 0 {
		return nil, usageError("mission requires a subcommand: clarification, question, or plan")
	}
	switch args[0] {
	case "clarification":
		if len(args) < 2 {
			return nil, usageError("mission clarification requires a subcommand: complete")
		}
		switch args[1] {
		case "complete":
			return missionClarificationComplete(ctx, socket, args[2:])
		default:
			return nil, usageError("unknown mission clarification command: " + args[1])
		}
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

func missionClarificationComplete(ctx context.Context, socket string, args []string) (protocol.MissionClarificationCompleteResult, error) {
	fs := newFlags("mission clarification complete")
	key := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.MissionClarificationCompleteResult{}, err
	}
	if *key == "" || fs.NArg() != 0 {
		return protocol.MissionClarificationCompleteResult{}, usageError("mission clarification complete requires --idempotency-key")
	}
	p := protocol.MissionClarificationCompleteParams{IdempotencyKey: *key}
	var out protocol.MissionClarificationCompleteResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMissionClarificationComplete, p, &out); err != nil {
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
  1. Decide whether the objective is specified well enough to plan. If not,
     ask the accountable human; this asks the human, not a peer run:
       aether-internal mission question ask --body 'question' --idempotency-key <key>
     Wait for answers; the call returns when something changes and also
     returns unchanged after the wait elapses. Repeat while open_questions
     is above zero. Waiting is not being blocked: do not report an outcome.
       aether-internal mission plan show --wait 30
  2. When you have what you need (questions are optional), say so:
       aether-internal mission clarification complete --idempotency-key <key>
     This is refused while a question you asked is unanswered.
  3. Check what is already proposed, then propose or revise:
       aether-internal task list --mission-id <mission-id>
       aether-internal task propose --mission-id <mission-id> \
         --idempotency-key <key> --revision-file /tmp/aether-task.json
     Declare expected_paths for every task: after approval, work outside
     them needs a human-approved amendment.
  4. Submit the plan for human review:
       aether-internal mission plan submit --summary 'what will be built and why' --idempotency-key <key>
No worker can start until a human approves the plan.
`

const clarifiedFlow = `Clarification is complete. Propose or revise tasks, then submit the plan:
  aether-internal task list --mission-id <mission-id>
  aether-internal task propose --mission-id <mission-id> --idempotency-key <key> --revision-file /tmp/aether-task.json
  aether-internal mission plan submit --summary 'what will be built and why' --idempotency-key <key>
Declare expected_paths for every task. No worker can start until a human
approves the plan.
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

const amendmentReviewWait = `A human is reviewing the amendment. Approved work continues: you may accept
submissions, inspect, cancel, and retry workers on approved tasks. Do not
propose, revise, abandon, or accept task revisions; the server refuses them.
Wait for the decision by repeating this call until the phase changes:
  aether-internal mission plan show --wait 30
Waiting is not being blocked and is not an outcome; do not run
aether-internal report. When the phase changes, run aether-internal skill
again.
`

const planRejected = `A human rejected the plan and this run is being cancelled. Do not start work,
do not change tasks, and do not report an outcome: the cancellation is the
terminal event.
`

const activeAmendments = `Changing the approved plan:
  A revision you accept yourself must stay within the approved plan: not
  marked "material": true, and expected_paths inside the approved paths. The
  server refuses task accept otherwise.
  A material change - new scope, new subsystems, more work, a changed
  constraint or success criterion - is proposed with "material": true, then
  submitted for human approval:
    aether-internal mission plan submit --summary 'what changes and why' --idempotency-key <key>
  Approved work keeps running while the human decides; the new work cannot
  start before approval. Cancel or wait for a worker on a task before you
  revise that task. Drop a pending revision with
    aether-internal task abandon --task-id <task> --revision <n> --expected-integrator-generation <g> --idempotency-key <key>
  Drop a whole task you proposed this round with the same command and no
  --revision:
    aether-internal task abandon --task-id <task> --expected-integrator-generation <g> --idempotency-key <key>
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
		return nil
	case "clarified":
		if _, err := fmt.Fprintf(out, "Phase: clarified\nPlan version: %d\n", assignment.PlanVersion); err != nil {
			return fmt.Errorf("write skill phase: %w", err)
		}
		if err := writeSkillFeedback(out, assignment.LatestFeedback); err != nil {
			return err
		}
		if _, err := io.WriteString(out, clarifiedFlow); err != nil {
			return fmt.Errorf("write skill clarified flow: %w", err)
		}
		return nil
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
		return nil
	case "rejected":
		if _, err := io.WriteString(out, "Phase: rejected\n"+planRejected); err != nil {
			return fmt.Errorf("write skill phase: %w", err)
		}
		return nil
	case "amendment_review":
		if _, err := fmt.Fprintf(out, "Phase: amendment_review\nPlan version: %d\n", assignment.PlanVersion); err != nil {
			return fmt.Errorf("write skill phase: %w", err)
		}
		if err := writeSkillFeedback(out, assignment.LatestFeedback); err != nil {
			return err
		}
		if _, err := io.WriteString(out, amendmentReviewWait); err != nil {
			return fmt.Errorf("write skill amendment review wait: %w", err)
		}
	case "active":
		if _, err := fmt.Fprintf(out, "Phase: active\nPlan version: %d (approved)\n", assignment.PlanVersion); err != nil {
			return fmt.Errorf("write skill phase: %w", err)
		}
		if _, err := io.WriteString(out, activeAmendments); err != nil {
			return fmt.Errorf("write skill amendment guidance: %w", err)
		}
	}
	// active and amendment_review both keep dispatching approved work, so both
	// continue into the candidate flow, as does an assignment with no phase.
	if _, err := io.WriteString(out, integratorWorkflow); err != nil {
		return fmt.Errorf("write skill integration workflow: %w", err)
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
