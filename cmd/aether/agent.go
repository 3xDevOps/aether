package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"golang.org/x/term"
)

func init() {
	register(command{
		name:  "agent",
		short: "manage agents: add, list, login, remove",
		usage: "aether agent list [--json]\n" +
			"aether agent login <name>\n" +
			"aether agent add <name> [--enhanced] [--standard <argv>] [--background <argv>] [--acp <argv>] [--acp-mode <id>] [--login <path>]\n" +
			"aether agent remove <name>",
		example: "aether agent list --json\n" +
			"aether agent login claude\n" +
			"aether agent add codex --enhanced\n" +
			"aether agent remove codex",
		run: runAgent,
	})
}

func runAgent(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether agent <add|list|login|remove>")
	}
	switch args[0] {
	case "add":
		return agentAdd(args[1:])
	case "list":
		return agentList(args[1:])
	case "login":
		return agentLogin(args[1:])
	case "remove":
		return agentRemove(args[1:])
	default:
		return fmt.Errorf("unknown agent command %q", args[0])
	}
}

func agentRemove(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: aether agent remove <name>")
	}
	return withControl(func(c *protocol.Client) error {
		var result protocol.AgentRemoveResult
		if err := c.Call(protocol.MethodAgentRemove, protocol.AgentRemoveParams{Name: args[0]}, &result); err != nil {
			return err
		}
		if len(result.Removed) == 0 {
			_, err := fmt.Fprintf(os.Stdout, "removed agent %s\n", args[0])
			return err
		}
		for _, path := range result.Removed {
			if _, err := fmt.Fprintf(os.Stdout, "removed %s\n", path); err != nil {
				return err
			}
		}
		return nil
	})
}

func agentList(args []string) error {
	jsonOut := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !jsonOut {
		return fmt.Errorf("usage: aether agent list [--json]")
	}
	return withControl(func(c *protocol.Client) error {
		var list protocol.AgentListResult
		if err := c.Call(protocol.MethodAgentList, struct{}{}, &list); err != nil {
			return err
		}
		if jsonOut {
			if list.Agents == nil {
				list.Agents = []protocol.AgentInfo{}
			}
			return json.NewEncoder(os.Stdout).Encode(list.Agents)
		}
		return printAgents(os.Stdout, list.Agents)
	})
}

// printAgents labels the login column for what agent.list checks: a login
// file in the home, not whether the vendor accepts it.
func printAgents(w io.Writer, agents []protocol.AgentInfo) error {
	if len(agents) == 0 {
		_, err := fmt.Fprintln(w, "no agents")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "AGENT\tSOURCE\tINSTALLED\tENHANCED\tLOGIN FILE")
	for _, a := range agents {
		installed, enhanced, login := "no", "none", "-"
		if a.Installed {
			installed = "yes"
		}
		if a.Enhanced != "none" {
			enhanced = "not installed"
			if a.EnhancedInstalled {
				enhanced = "installed"
			}
		}
		if a.LoginFound != nil {
			login = "not found"
			if *a.LoginFound {
				login = "found"
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Source, installed, enhanced, login)
	}
	return tw.Flush()
}

func lookupAgent(c *protocol.Client, name string) (protocol.AgentInfo, bool, error) {
	var list protocol.AgentListResult
	if err := c.Call(protocol.MethodAgentList, struct{}{}, &list); err != nil {
		return protocol.AgentInfo{}, false, err
	}
	for _, agent := range list.Agents {
		if agent.Name == name {
			return agent, true, nil
		}
	}
	return protocol.AgentInfo{}, false, nil
}

type agentAddOptions struct {
	name       string
	standard   string
	background string
	acp        string
	acpMode    string
	login      stringList
	enhanced   bool
}

func parseAgentAdd(args []string) (agentAddOptions, error) {
	fs := newFlagSet("agent add")
	standard := fs.String("standard", "", "Standard mode command template")
	background := fs.String("background", "", "Background mode command template")
	acp := fs.String("acp", "", "Agent Client Protocol server command")
	acpMode := fs.String("acp-mode", "", "ACP session mode an Enhanced run starts in")
	var login stringList
	fs.Var(&login, "login", "file whose presence shows the agent is logged in, such as ~/.cursor/auth.json (repeatable)")
	enhanced := fs.Bool("enhanced", false, "also install the shipped agent's enhanced-mode adapter")
	const usage = "usage: aether agent add <name> [--enhanced] [--standard <argv>] [--background <argv>] [--acp <argv>] [--acp-mode <id>] [--login <path>]"
	name, err := parseLeadingArg(fs, args)
	if err != nil {
		return agentAddOptions{}, fmt.Errorf("%w\n%s", err, usage)
	}
	if name == "" {
		return agentAddOptions{}, errors.New(usage)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for i, path := range login {
			// The caller's shell expands ~ to its own home, which no run has.
			if rest, ok := strings.CutPrefix(path, home+"/"); ok {
				login[i] = "~/" + rest
			}
		}
	}
	return agentAddOptions{name: name, standard: *standard, background: *background, acp: *acp, acpMode: *acpMode, login: login, enhanced: *enhanced}, nil
}

// resolveAgentArgs turns flag values into argv templates. Shipped names send
// no proposal; the server already knows their argv. For custom names a missing
// flag prompts on promptInput with the default shown, and a nil promptInput
// (no terminal) takes the default silently.
func resolveAgentArgs(name, standardFlag, backgroundFlag string, shipped bool, promptInput io.Reader) (standard, background []string, err error) {
	if shipped {
		return nil, nil, nil
	}
	var lines *bufio.Reader
	if promptInput != nil {
		lines = bufio.NewReader(promptInput)
	}
	resolve := func(flagValue, label, def string) ([]string, error) {
		if flagValue != "" {
			fields := strings.Fields(flagValue)
			if len(fields) == 0 {
				return nil, fmt.Errorf("--%s is blank", strings.ToLower(label))
			}
			return fields, nil
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
	if standard, err = resolve(standardFlag, "Standard", name+" {task}"); err != nil {
		return nil, nil, err
	}
	if background, err = resolve(backgroundFlag, "Background", standard[0]+" -p {task}"); err != nil {
		return nil, nil, err
	}
	return standard, background, nil
}

func agentAdd(args []string) error {
	opts, err := parseAgentAdd(args)
	if err != nil {
		return err
	}
	var selected protocol.AgentInfo
	found := false
	if listErr := withControl(func(c *protocol.Client) error {
		var lookupErr error
		selected, found, lookupErr = lookupAgent(c, opts.name)
		return lookupErr
	}); listErr != nil {
		return listErr
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	if found && selected.Source == "shipped" {
		if opts.acp != "" {
			return fmt.Errorf("agent %s is shipped; its ACP server is fixed, so --acp applies only to your own agent", opts.name)
		}
		if opts.enhanced && selected.Enhanced == "none" {
			return fmt.Errorf("agent %s has no enhanced mode", opts.name)
		}
		if interactive {
			err = typeInTerminal(agentInstallScript(selected, opts.enhanced))
		} else {
			err = installShippedAgent(os.Stdout, selected, opts.enhanced)
		}
		if err != nil {
			return err
		}
		if selected.Name == "codex" {
			if _, err = fmt.Fprintln(os.Stdout, codexHookNote); err != nil {
				return err
			}
		}
		if !interactive {
			_, err = fmt.Fprintf(os.Stdout, "sign in with: aether agent login %s\n", selected.Name)
		}
		return err
	}
	if opts.enhanced {
		return fmt.Errorf("--enhanced installs a shipped agent's adapter; name your agent's ACP server command with --acp")
	}
	var promptInput io.Reader
	if interactive {
		promptInput = os.Stdin
	}
	standardArgs, backgroundArgs, err := resolveAgentArgs(opts.name, opts.standard, opts.background, false, promptInput)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		var result protocol.AgentRegisterResult
		if err := c.Call(protocol.MethodAgentRegister, protocol.AgentRegisterParams{
			Definition: protocol.AgentDefinition{
				Name:            opts.name,
				Executable:      standardArgs[0],
				TUIArgs:         standardArgs,
				HeadlessArgs:    backgroundArgs,
				ACPArgs:         strings.Fields(opts.acp),
				ACPMode:         opts.acpMode,
				CredentialPaths: opts.login,
			},
		}, &result); err != nil {
			return err
		}
		_, err := fmt.Fprintf(os.Stdout, "agent %s registered\n", opts.name)
		return err
	})
}

const codexHookNote = `Trust Aether's hooks once. Standard Codex runs load Aether's inbox hooks. Codex runs a hook
only after you trust it, so your first Standard run opens "Hooks need review": choose
"Trust all and continue". Codex remembers this in ~/.codex/config.toml for every workspace and
asks again only if Aether's hooks change. If a swarm worker is your first Standard Codex run,
open its terminal to answer. If the list shows hooks you don't recognize, choose "Review hooks"
instead.`

func installShippedAgent(w io.Writer, agent protocol.AgentInfo, enhanced bool) error {
	var result protocol.AgentInstallResult
	if err := withControl(func(c *protocol.Client) error {
		return c.Call(protocol.MethodAgentInstall, protocol.AgentInstallParams{Name: agent.Name, Enhanced: enhanced}, &result)
	}); err != nil {
		return err
	}
	if tail := strings.TrimRight(result.LogTail, "\n"); tail != "" {
		if _, err := fmt.Fprintln(w, tail); err != nil {
			return err
		}
	}
	switch {
	case result.Error != "":
		return fmt.Errorf("agent %s: %s; its output is above", agent.Name, result.Error)
	case !result.Installed:
		return fmt.Errorf("agent %s: the install command succeeded, but ~/.local/bin/%s is missing; its output is above", agent.Name, agent.Executable)
	case enhanced && !result.EnhancedInstalled:
		return fmt.Errorf("agent %s: installed, but its Enhanced adapter is missing from ~/.local/bin; its output is above", agent.Name)
	case enhanced:
		_, err := fmt.Fprintf(w, "installed %s and its Enhanced adapter\n", agent.Name)
		return err
	}
	_, err := fmt.Fprintf(w, "installed %s\n", agent.Name)
	return err
}

// typeInTerminal needs a person at this terminal: it leaves them in the
// Environment's shell.
func typeInTerminal(command string) error {
	cfg, err := cli.Load()
	if err != nil {
		return terminalGuidanceError("load config", err, command)
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return terminalGuidanceError("connect", err, command)
	}
	defer func() { _ = conn.Close() }()

	cols, rows := termSize()
	stream, _, err := conn.TerminalStream(protocol.TerminalRequest{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		return terminalGuidanceError("open the environment terminal", err, command)
	}
	defer func() { _ = stream.Close() }()

	if _, err := io.WriteString(stream, command+"\n"); err != nil {
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

func terminalGuidanceError(step string, err error, command string) error {
	return fmt.Errorf("%s: %w; run `aether terminal`, then paste:\n%s", step, err, command)
}
