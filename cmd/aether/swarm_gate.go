package main

import (
	"cmp"
	"errors"
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

const swarmAnswerUsage = "usage: aether swarm answer <swarm-id> --question <question-id> \"<answer>\"|-"

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
	fs := newFlagSet("swarm answer")
	question := fs.String("question", "", "ID of the question to answer (from aether swarm show)")
	answer, err = parseLeadingArg(fs, rest)
	if err != nil {
		return "", "", "", fmt.Errorf("%w\n%s", err, swarmAnswerUsage)
	}
	if *question == "" {
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

func swarmCancel(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: aether swarm cancel <swarm-id>")
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
	if _, err := fmt.Fprintf(w, "swarm %s %s\n", res.Mission.ID, res.Mission.Phase); err != nil {
		return fmt.Errorf("write swarm result: %w", err)
	}
	return nil
}

const swarmReplaceUsage = "usage: aether swarm replace-integrator <swarm-id> --agent <agent> [--mode standard|enhanced] [--model <model>] [--account <member-id>]"

func swarmReplaceIntegrator(args []string) error {
	missionID, rest, err := swarmTarget(args, swarmReplaceUsage)
	if err != nil {
		return err
	}
	fs := newFlagSet("swarm replace-integrator")
	agent := fs.String("agent", "", "agent of the new integrator")
	mode := fs.String("mode", "", "integrator mode: standard or enhanced (default: "+swarmModeDefault+")")
	model := fs.String("model", "", "integrator "+modelHelp)
	account := fs.String("account", "", "member ID whose shared agent account to use (default: the current integrator's)")
	if err = fs.Parse(rest); err != nil {
		return err
	}
	if *agent == "" || fs.NArg() != 0 {
		return errors.New(swarmReplaceUsage)
	}
	integratorMode, err := parseIntegratorMode(*mode)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		return replaceSwarmIntegrator(c, os.Stdout, missionID, protocol.MissionIntegrator{AccountMemberID: *account, Harness: *agent, Mode: integratorMode, Model: *model}, cli.NewControlSessionID())
	})
}

// replaceSwarmIntegrator replaces the integrator generation show reports, so
// a replacement another human made in the meantime is not replaced again.
func replaceSwarmIntegrator(c *protocol.Client, w io.Writer, missionID string, integrator protocol.MissionIntegrator, key string) error {
	shown, err := showSwarm(c, missionID)
	if err != nil {
		return err
	}
	integrator.AccountMemberID = cmp.Or(integrator.AccountMemberID, shown.Mission.Integrator.AccountMemberID)
	if integrator.Mode == "" {
		mode, modesErr := swarmModes(c, integrator.AccountMemberID)
		if modesErr != nil {
			return modesErr
		}
		if integrator.Mode, err = mode(integrator.Harness); err != nil {
			return err
		}
	}
	params := protocol.MissionReplaceIntegratorParams{
		MissionID:          missionID,
		ExpectedGeneration: shown.Mission.IntegratorGeneration,
		Integrator:         integrator,
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
