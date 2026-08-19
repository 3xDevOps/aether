package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "run",
		short: "launch an agent run",
		run:   runRun,
	})
}

func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	agent := fs.String("agent", "", "harness name")
	mode := fs.String("mode", "tui", "tui or headless")
	session := fs.String("session", "", "session ID (default: the only session)")
	template := fs.String("template", "", "launch a saved task template instead of a prompt")
	params := kvFlag{}
	fs.Var(params, "param", "value for a template parameter, name=value (repeatable)")

	// A template launch carries no prompt, so the leading positional
	// argument is optional here.
	task := ""
	if len(args) > 0 && args[0] != "--" && !strings.HasPrefix(args[0], "-") {
		task, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if task == "" && fs.NArg() == 1 {
		task = fs.Arg(0)
	}
	if *template != "" {
		if task != "" || *agent != "" {
			return fmt.Errorf("--template launches a saved definition: drop the task prompt and --agent")
		}
		return launchTemplate(*session, *template, params)
	}
	if task == "" || *agent == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: aether run \"task\" --agent <name> [--mode tui|headless] [--session]\n   or: aether run --template <name> [--param k=v] [--session]")
	}
	if *mode != "tui" && *mode != "headless" {
		return fmt.Errorf("invalid mode %q (want tui or headless)", *mode)
	}
	return withControl(func(c *protocol.Client) error {
		sessID, err := resolveSession(c, *session)
		if err != nil {
			return err
		}
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
			SessionID: sessID,
			Task:      task,
			Harness:   *agent,
			Mode:      *mode,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("run %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}

func resolveSession(c *protocol.Client, idOrName string) (string, error) {
	var sl protocol.SessionListResult
	if err := c.Call(protocol.MethodSessionList, struct{}{}, &sl); err != nil {
		return "", err
	}
	if idOrName == "" {
		if len(sl.Sessions) == 1 {
			return sl.Sessions[0].ID, nil
		}
		if len(sl.Sessions) == 0 {
			return "", fmt.Errorf("no sessions; create one with aether session new")
		}
		return "", fmt.Errorf("--session is required when more than one session exists")
	}
	for _, s := range sl.Sessions {
		if s.ID == idOrName || s.Name == idOrName {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("session %q not found", idOrName)
}
