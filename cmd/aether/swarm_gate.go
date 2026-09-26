package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// swarmTarget splits the mission ID from the arguments that follow it.
func swarmTarget(args []string, usage string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, errors.New(usage)
	}
	return args[0], args[1:], nil
}

const swarmAnswerUsage = "usage: aether swarm answer <mission-id> --question <question-id> \"<answer>\"|-"

func swarmAnswer(args []string, stdin io.Reader) error {
	missionID, questionID, answer, err := parseSwarmAnswer(args, stdin)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		return answerSwarmQuestion(c, os.Stdout, missionID, questionID, answer, cli.NewControlSessionID())
	})
}

func parseSwarmAnswer(args []string, stdin io.Reader) (missionID, questionID, answer string, err error) {
	missionID, rest, err := swarmTarget(args, swarmAnswerUsage)
	if err != nil {
		return "", "", "", err
	}
	fs := flag.NewFlagSet("swarm answer", flag.ExitOnError)
	question := fs.String("question", "", "ID of the question to answer (from aether swarm show)")
	answer, err = parseLeadingArg(fs, rest)
	if err != nil || *question == "" {
		return "", "", "", errors.New(swarmAnswerUsage)
	}
	answer, err = stdinText(answer, "answer", stdin)
	if err != nil {
		return "", "", "", err
	}
	return missionID, *question, answer, nil
}

// answerSwarmQuestion answers only a question the named swarm asked: the
// server resolves the mission from the question ID alone, so a question ID
// from another swarm would otherwise be answered there.
func answerSwarmQuestion(c *protocol.Client, w io.Writer, missionID, questionID, answer, key string) error {
	shown, err := showSwarm(c, missionID)
	if err != nil {
		return err
	}
	asked := false
	for _, q := range shown.Questions {
		if q.ID == questionID {
			asked = true
			break
		}
	}
	if !asked {
		return fmt.Errorf("swarm %s has no question %s", missionID, questionID)
	}
	var res protocol.MissionQuestionResult
	params := protocol.MissionQuestionAnswerParams{QuestionID: questionID, Answer: answer, IdempotencyKey: key}
	if err := c.Call(protocol.MethodMissionQuestionAnswer, params, &res); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "question %s answered\n", res.Question.ID); err != nil {
		return fmt.Errorf("write swarm result: %w", err)
	}
	return nil
}

// swarmDecisionUsage maps a mission.plan.decide decision to the command that
// sends it.
var swarmDecisionUsage = map[string]string{
	"approve": "usage: aether swarm approve <mission-id>",
	"revise":  "usage: aether swarm request-changes <mission-id> \"<feedback>\"",
	"reject":  "usage: aether swarm reject <mission-id> [\"<feedback>\"]",
}

func swarmDecide(decision string, args []string) error {
	missionID, feedback, err := parseSwarmDecision(decision, args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		return decideSwarmPlan(c, os.Stdout, missionID, decision, feedback, cli.NewControlSessionID())
	})
}

// parseSwarmDecision rejects what the server would refuse before any RPC:
// feedback is required to request changes and not accepted on approve.
func parseSwarmDecision(decision string, args []string) (missionID, feedback string, err error) {
	usage := swarmDecisionUsage[decision]
	missionID, rest, err := swarmTarget(args, usage)
	if err != nil {
		return "", "", err
	}
	switch {
	case len(rest) == 0 && decision != "revise":
	case len(rest) == 1 && decision != "approve" && strings.TrimSpace(rest[0]) != "":
		feedback = rest[0]
	default:
		return "", "", errors.New(usage)
	}
	return missionID, feedback, nil
}

// decideSwarmPlan decides the plan version show reports, so a plan the
// integrator resubmitted since the human read it is not decided unread.
func decideSwarmPlan(c *protocol.Client, w io.Writer, missionID, decision, feedback, key string) error {
	shown, err := showSwarm(c, missionID)
	if err != nil {
		return err
	}
	if shown.Mission.PlanVersion == 0 {
		return fmt.Errorf("swarm %s has no submitted plan to decide (phase %s)", missionID, shown.Mission.Phase)
	}
	params := protocol.MissionPlanDecideParams{
		MissionID:           missionID,
		ExpectedPlanVersion: shown.Mission.PlanVersion,
		Decision:            decision,
		Feedback:            feedback,
		IdempotencyKey:      key,
	}
	var res protocol.MissionPlanDecideResult
	if err := c.Call(protocol.MethodMissionPlanDecide, params, &res); err != nil {
		return err
	}
	return printSwarmPhase(w, res.Mission)
}

func swarmCancel(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: aether swarm cancel <mission-id>")
	}
	return withControl(func(c *protocol.Client) error {
		return cancelSwarm(c, os.Stdout, args[0], cli.NewControlSessionID())
	})
}

func cancelSwarm(c *protocol.Client, w io.Writer, missionID, key string) error {
	var res protocol.MissionCancelResult
	if err := c.Call(protocol.MethodMissionCancel, protocol.MissionCancelParams{MissionID: missionID, IdempotencyKey: key}, &res); err != nil {
		return err
	}
	return printSwarmPhase(w, res.Mission)
}

const swarmReplaceUsage = "usage: aether swarm replace-integrator <mission-id> --agent <harness> [--account <member-id>]"

func swarmReplaceIntegrator(args []string) error {
	missionID, rest, err := swarmTarget(args, swarmReplaceUsage)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("swarm replace-integrator", flag.ExitOnError)
	agent := fs.String("agent", "", "harness of the new integrator (runs in tui mode)")
	account := fs.String("account", "", "member ID whose shared agent account to use (default: the current integrator's)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *agent == "" || fs.NArg() != 0 {
		return errors.New(swarmReplaceUsage)
	}
	return withControl(func(c *protocol.Client) error {
		return replaceSwarmIntegrator(c, os.Stdout, missionID, *agent, *account, cli.NewControlSessionID())
	})
}

// replaceSwarmIntegrator replaces the integrator generation show reports, so
// a replacement another human made in the meantime is not replaced again.
func replaceSwarmIntegrator(c *protocol.Client, w io.Writer, missionID, agent, account, key string) error {
	shown, err := showSwarm(c, missionID)
	if err != nil {
		return err
	}
	if account == "" {
		account = shown.Mission.Integrator.AccountMemberID
	}
	params := protocol.MissionReplaceIntegratorParams{
		MissionID:          missionID,
		ExpectedGeneration: shown.Mission.IntegratorGeneration,
		Integrator:         protocol.MissionIntegrator{AccountMemberID: account, Harness: agent, Mode: "tui"},
		IdempotencyKey:     key,
	}
	var res protocol.MissionReplaceIntegratorResult
	if err := c.Call(protocol.MethodMissionReplaceIntegrator, params, &res); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "swarm %s %s\nintegrator run %s\n", res.Mission.ID, res.Mission.Phase, res.RunID); err != nil {
		return fmt.Errorf("write swarm result: %w", err)
	}
	return nil
}

func printSwarmPhase(w io.Writer, m protocol.Mission) error {
	if _, err := fmt.Fprintf(w, "swarm %s %s\n", m.ID, m.Phase); err != nil {
		return fmt.Errorf("write swarm result: %w", err)
	}
	return nil
}
