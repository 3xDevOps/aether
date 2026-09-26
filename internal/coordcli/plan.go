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

const integratorRole = "You are this mission's integrator: turn the objective into tasks for workers and coordinate them; do not implement the objective yourself. Workers reach you with send and ask; answer with reply. An aether: line in your terminal means a message or mission event is waiting and names the command that reads it.\n"

const planningFlow = `Next: clarify the objective before submitting a plan.
If you need an answer from the accountable human (not a peer):
  aether-internal mission question ask --help
Read questions and wait for answers:
  aether-internal mission plan show --wait 30
Questions are optional. Once the objective is clear and no question is open:
  aether-internal mission clarification complete --idempotency-key clarify-1
Then rerun aether-internal skill. Reuse clarify-1 only to replay this operation;
choose a fresh key for a later clarification round. Do not report while waiting.
No worker starts until a human approves the plan.
`

const clarifiedFlow = `Next: propose or revise tasks, then submit the plan for human review.
Discover the required flags and a minimal valid revision JSON:
  aether-internal task propose --help
  aether-internal task revise --help
  aether-internal mission plan submit --help
Declare scope.expected_paths for every task and retain its exclusions.
Submit only after the drafts are ready; no worker starts before human approval.
`

const planReviewWait = `Next: wait for the human's plan decision.
  aether-internal mission plan show --wait 30
Repeat this bounded wait until the phase changes, then rerun aether-internal skill.
Do not mutate tasks or start workers while the plan is frozen.
Waiting is not an outcome: do not report. If this run stops, a human uses
Replace integrator; the agent cannot take over or approve the plan.
`

const amendmentReviewWait = `Next: wait for the human's amendment decision; approved work may continue.
  aether-internal mission plan show --wait 30
Repeat until the phase changes, then rerun aether-internal skill.
You may accept submissions and manage workers on the already-approved set.
Do not propose, revise, abandon, or accept task revisions while review is open.
No new work under review may start. Waiting is not an outcome: do not report.
`

const planRejected = `The human rejected the plan; this run is being cancelled.
Do not start work, mutate tasks, or report an outcome. Cancellation is terminal.
`

const activeAmendments = `Next: dispatch approved tasks and review their submissions.
  aether-internal worker start --help
  aether-internal task accept-submission --help
Use current task revisions, integrator generation, accepted-set version, and
approved execution choices from live results; never guess IDs or generations.
Changes outside approved scope, dropped exclusions, material changes, and new
tasks require a human-approved amendment. Set "material":true when changing
scope, constraints, or success criteria; do not accept those revisions yourself.
Cancel or wait for a worker before revising its task. To discover amendment syntax:
  aether-internal task revise --help
  aether-internal mission plan submit --help
  aether-internal task abandon --help
Approved work may continue during amendment review, but new work waits for approval.
`

// writeSkillPhase prints only the immediate instructions for the current phase.
func writeSkillPhase(out io.Writer, assignment *protocol.CoordMissionAssignment) error {
	if _, err := fmt.Fprintf(out, "Phase: %s\nPlan version: %d\nOpen questions: %d\n",
		boundedSkillField(assignment.Phase), assignment.PlanVersion, assignment.OpenQuestions); err != nil {
		return fmt.Errorf("write skill phase: %w", err)
	}
	if err := writeSkillFeedback(out, assignment.LatestFeedback); err != nil {
		return err
	}
	var flow string
	switch assignment.Phase {
	case "planning":
		flow = planningFlow
	case "clarified":
		flow = clarifiedFlow
	case "plan_review":
		flow = planReviewWait
	case "active":
		flow = activeAmendments
	case "amendment_review":
		flow = amendmentReviewWait
	case "rejected":
		flow = planRejected
	default:
		flow = "No recognized plan phase supplied. Refresh aether-internal status before mission actions.\n"
	}
	if _, err := io.WriteString(out, flow); err != nil {
		return fmt.Errorf("write skill phase guidance: %w", err)
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
