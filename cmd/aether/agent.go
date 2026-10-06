package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
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
	acp      string
	enhanced bool
}

func parseAgentAdd(args []string) (agentAddOptions, error) {
	fs := flag.NewFlagSet("agent add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tui := fs.String("tui", "", "interactive command template")
	headless := fs.String("headless", "", "headless command template")
	acp := fs.String("acp", "", "Agent Client Protocol server command")
	enhanced := fs.Bool("enhanced", false, "also install the shipped agent's enhanced-mode adapter")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" {
		return agentAddOptions{}, fmt.Errorf("usage: aether agent add <name> [--enhanced] [--tui <argv>] [--headless <argv>] [--acp <argv>]")
	}
	return agentAddOptions{name: name, tui: *tui, headless: *headless, acp: *acp, enhanced: *enhanced}, nil
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
		if opts.acp != "" {
			return fmt.Errorf("agent %s is shipped; its ACP server is fixed, so --acp applies only to your own agent", opts.name)
		}
		if opts.enhanced && selected.Enhanced == "none" {
			return fmt.Errorf("agent %s has no enhanced mode", opts.name)
		}
		return runShippedAgentInstall(selected, opts.enhanced)
	}
	if opts.enhanced {
		return fmt.Errorf("--enhanced installs a shipped agent's adapter; name your agent's ACP server command with --acp")
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
				ACPArgs:      strings.Fields(opts.acp),
			},
		}, &result); err != nil {
			return err
		}
		return nil
	})
}

func runShippedAgentInstall(agent protocol.AgentInfo, enhanced bool) error {
	script := agentInstallScript(agent, enhanced)
	cfg, err := cli.Load()
	if err != nil {
		return printAgentInstallGuidance(os.Stdout, script)
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return printAgentInstallGuidance(os.Stdout, script)
	}
	defer func() { _ = conn.Close() }()

	cols, rows := termSize()
	stream, _, err := conn.TerminalStream(protocol.TerminalRequest{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		return printAgentInstallGuidance(os.Stdout, script)
	}
	defer func() { _ = stream.Close() }()

	if _, err := io.WriteString(stream, script+"\n"); err != nil {
		return err
	}
	// A member terminal is the member's own and records no steerer, so
	// there is no replay window to mute.
	return describeTerminalEnd(copyRaw(stream, 0))
}

func agentInstallScript(agent protocol.AgentInfo, enhanced bool) string {
	switch {
	case enhanced && agent.EnhancedInstallScript != "":
		return agent.EnhancedInstallScript
	case agent.InstallScript != "":
		return agent.InstallScript
	}
	return fmt.Sprintf("install %s into ~/.local/bin", agent.Name)
}

func printAgentInstallGuidance(w io.Writer, script string) error {
	_, err := fmt.Fprintf(w, "Run `aether terminal`, then paste:\n%s\n", script)
	return err
}
