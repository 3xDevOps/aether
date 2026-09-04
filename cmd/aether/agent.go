package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
	"golang.org/x/term"
)

func init() {
	register(command{
		name:  "agent",
		short: "manage agents: add, list",
		run:   runAgent,
	})
}

func runAgent(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether agent <add|list>")
	}
	switch args[0] {
	case "add":
		return agentAdd(args[1:])
	case "list":
		return agentList(args[1:])
	default:
		return fmt.Errorf("unknown agent command %q", args[0])
	}
}

func agentList(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: aether agent list")
	}
	return withControl(func(c *protocol.Client) error {
		var list protocol.AgentListResult
		if err := c.Call(protocol.MethodAgentList, struct{}{}, &list); err != nil {
			return err
		}
		return printAgents(os.Stdout, list.Agents)
	})
}

// printAgents emits one "agent <name> <source>" line per agent so members can
// tell shipped profiles from their own registered definitions.
func printAgents(w io.Writer, agents []protocol.AgentInfo) error {
	if len(agents) == 0 {
		_, err := fmt.Fprintln(w, "no agents")
		return err
	}
	for _, a := range agents {
		if _, err := fmt.Fprintf(w, "agent %s %s\n", a.Name, a.Source); err != nil {
			return err
		}
	}
	return nil
}

type agentAddOptions struct {
	name     string
	tui      string
	headless string
}

func parseAgentAdd(args []string) (agentAddOptions, error) {
	fs := flag.NewFlagSet("agent add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tui := fs.String("tui", "", "interactive command template")
	headless := fs.String("headless", "", "headless command template")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" {
		return agentAddOptions{}, fmt.Errorf("usage: aether agent add <name> [--tui <argv>] [--headless <argv>]")
	}
	return agentAddOptions{name: name, tui: *tui, headless: *headless}, nil
}

// resolveAgentArgs turns flag values into argv templates. Shipped names send
// no proposal; the server already knows their argv. For custom names a missing
// flag prompts on promptInput with the default shown, and a nil promptInput
// (no terminal) takes the default silently.
func resolveAgentArgs(name, tuiFlag, headlessFlag string, shipped bool, promptInput io.Reader) (tui, headless []string, err error) {
	if shipped {
		return nil, nil, nil
	}
	var lines *bufio.Reader
	if promptInput != nil {
		lines = bufio.NewReader(promptInput)
	}
	resolve := func(flagValue, label, def string) ([]string, error) {
		if flagValue != "" {
			return strings.Fields(flagValue), nil
		}
		if lines != nil {
			fmt.Fprintf(os.Stderr, "%s command [%s]: ", label, def)
			line, readErr := lines.ReadString('\n')
			if readErr != nil && readErr != io.EOF {
				return nil, readErr
			}
			if line = strings.TrimSpace(line); line != "" {
				return strings.Fields(line), nil
			}
		}
		return strings.Fields(def), nil
	}
	if tui, err = resolve(tuiFlag, "TUI", name+" {task}"); err != nil {
		return nil, nil, err
	}
	if headless, err = resolve(headlessFlag, "Headless", name+" -p {task}"); err != nil {
		return nil, nil, err
	}
	return tui, headless, nil
}

func agentAdd(args []string) error {
	opts, err := parseAgentAdd(args)
	if err != nil {
		return err
	}
	var selected protocol.AgentInfo
	found := false
	if listErr := withControl(func(c *protocol.Client) error {
		var list protocol.AgentListResult
		if callErr := c.Call(protocol.MethodAgentList, struct{}{}, &list); callErr != nil {
			return callErr
		}
		for _, agent := range list.Agents {
			if agent.Name == opts.name {
				selected, found = agent, true
				break
			}
		}
		return nil
	}); listErr != nil {
		return listErr
	}
	if found && selected.Source == "shipped" {
		return printAgentInstallGuidance(os.Stdout, selected)
	}
	var promptInput io.Reader
	if term.IsTerminal(int(os.Stdin.Fd())) {
		promptInput = os.Stdin
	}
	tuiArgs, headlessArgs, err := resolveAgentArgs(opts.name, opts.tui, opts.headless, false, promptInput)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		var result protocol.AgentRegisterResult
		if err := c.Call(protocol.MethodAgentRegister, protocol.AgentRegisterParams{
			Definition: protocol.AgentDefinition{
				Name:         opts.name,
				Executable:   opts.name,
				TUIArgs:      tuiArgs,
				HeadlessArgs: headlessArgs,
			},
		}, &result); err != nil {
			return err
		}
		return nil
	})
}

func printAgentInstallGuidance(w io.Writer, agent protocol.AgentInfo) error {
	script := agent.InstallScript
	if script == "" {
		script = fmt.Sprintf("install %s into ~/.local/bin", agent.Name)
	}
	_, err := fmt.Fprintf(w, "Run `aether terminal`, then paste:\n%s\n", script)
	return err
}
