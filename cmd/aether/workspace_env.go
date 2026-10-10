package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	workspaceEnvUsage       = "usage: aether workspace env <list|set|unset|import|script>"
	workspaceEnvListUsage   = "usage: aether workspace env list [--workspace <name-or-id>]"
	workspaceEnvSetUsage    = "usage: aether workspace env set [--workspace <name-or-id>] <NAME>=<value> [<NAME>=<value> ...]\n   or: aether workspace env set [--workspace <name-or-id>] --secret <NAME>   (the value is read from stdin)"
	workspaceEnvUnsetUsage  = "usage: aether workspace env unset [--workspace <name-or-id>] <NAME> [<NAME> ...]"
	workspaceEnvImportUsage = "usage: aether workspace env import [--workspace <name-or-id>] [--plain] <file|->"
	workspaceEnvScriptUsage = "usage: aether workspace env script [--workspace <name-or-id>] [--file <path|->|--clear]"

	workspaceEnvScope = "new runs get this; containers that already exist keep what they started with"
)

func workspaceEnv(args []string) error {
	if len(args) < 1 {
		return errors.New(workspaceEnvUsage)
	}
	switch args[0] {
	case "list":
		return workspaceEnvList(args[1:])
	case "set":
		return workspaceEnvSet(args[1:], os.Stdin)
	case "unset":
		return workspaceEnvUnset(args[1:])
	case "import":
		return workspaceEnvImport(args[1:], os.Stdin)
	case "script":
		return workspaceEnvScript(args[1:], os.Stdin)
	default:
		return fmt.Errorf("unknown workspace env command %q", args[0])
	}
}

func workspaceEnvFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs, fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
}

func workspaceEnvList(args []string) error {
	fs, workspace := workspaceEnvFlags("workspace env list")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New(workspaceEnvListUsage)
	}
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, *workspace, false)
		if err != nil {
			return err
		}
		var res protocol.WorkspaceEnvironmentResult
		if err := c.Call(protocol.MethodWorkspaceEnvironmentGet, protocol.WorkspaceEnvironmentGetParams{WorkspaceID: ws.ID}, &res); err != nil {
			return err
		}
		return printWorkspaceVariables(os.Stdout, ws, res)
	})
}

// changeWorkspaceEnv sends one change and prints the variables it leaves.
func changeWorkspaceEnv(workspace string, change protocol.WorkspaceEnvironmentSetParams) error {
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, workspace, false)
		if err != nil {
			return err
		}
		return sendWorkspaceEnv(c, ws, change)
	})
}

func sendWorkspaceEnv(c *protocol.Client, ws protocol.Workspace, change protocol.WorkspaceEnvironmentSetParams) error {
	change.WorkspaceID = ws.ID
	var res protocol.WorkspaceEnvironmentResult
	if err := c.Call(protocol.MethodWorkspaceEnvironmentSet, change, &res); err != nil {
		return err
	}
	if err := printWorkspaceVariables(os.Stdout, ws, res); err != nil {
		return err
	}
	_, err := fmt.Fprintln(os.Stdout, workspaceEnvScope)
	return err
}

// parseWorkspaceEnvSet returns the plain variables to set, or the one name
// whose secret value is still to be read from stdin.
func parseWorkspaceEnvSet(args []string) (workspace string, variables []protocol.WorkspaceVariable, secret string, err error) {
	fs, named := workspaceEnvFlags("workspace env set")
	asSecret := fs.Bool("secret", false, "store the variable as a secret; its value is read from stdin")
	if parseErr := fs.Parse(args); parseErr != nil || fs.NArg() == 0 {
		return "", nil, "", errors.New(workspaceEnvSetUsage)
	}
	if *asSecret {
		// A value on the command line would stay in shell history.
		if fs.NArg() != 1 || strings.Contains(fs.Arg(0), "=") {
			return "", nil, "", errors.New("--secret takes one NAME and reads its value from stdin, never from the command line:\n  aether workspace env set --secret <NAME> < <file>")
		}
		return *named, nil, fs.Arg(0), nil
	}
	for _, arg := range fs.Args() {
		name, value, ok := strings.Cut(arg, "=")
		if !ok || name == "" {
			return "", nil, "", errors.New(workspaceEnvSetUsage)
		}
		variables = append(variables, protocol.WorkspaceVariable{Name: name, Value: value})
	}
	return *named, variables, "", nil
}

// readSecretValue prompts without echo on a terminal, and otherwise takes
// stdin whole, less the newline a shell pipeline adds.
func readSecretValue(name string, stdin io.Reader) (string, error) {
	var raw []byte
	var err error
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprintf(os.Stderr, "Value for %s (input hidden): ", name)
		raw, err = term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(os.Stderr)
	} else {
		raw, err = io.ReadAll(stdin)
	}
	if err != nil {
		return "", fmt.Errorf("read the value of %s from stdin: %w", name, err)
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if value == "" {
		return "", fmt.Errorf("the value of %s on stdin is empty", name)
	}
	return value, nil
}

func workspaceEnvSet(args []string, stdin io.Reader) error {
	workspace, variables, secret, err := parseWorkspaceEnvSet(args)
	if err != nil {
		return err
	}
	if secret == "" {
		return changeWorkspaceEnv(workspace, protocol.WorkspaceEnvironmentSetParams{Set: variables})
	}
	// The workspace is resolved first, so a wrong one is refused before
	// anyone types a secret.
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, workspace, false)
		if err != nil {
			return err
		}
		value, err := readSecretValue(secret, stdin)
		if err != nil {
			return err
		}
		return sendWorkspaceEnv(c, ws, protocol.WorkspaceEnvironmentSetParams{
			Set: []protocol.WorkspaceVariable{{Name: secret, Value: value, Secret: true}},
		})
	})
}

func workspaceEnvUnset(args []string) error {
	fs, workspace := workspaceEnvFlags("workspace env unset")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return errors.New(workspaceEnvUnsetUsage)
	}
	return changeWorkspaceEnv(*workspace, protocol.WorkspaceEnvironmentSetParams{Unset: fs.Args()})
}

func parseWorkspaceEnvImport(args []string, stdin io.Reader) (string, []protocol.WorkspaceVariable, error) {
	fs, workspace := workspaceEnvFlags("workspace env import")
	plain := fs.Bool("plain", false, "store the values as plain variables instead of secrets")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return "", nil, errors.New(workspaceEnvImportUsage)
	}
	source := fs.Arg(0)
	text, err := readFileOrStdin(source, stdin)
	if err != nil {
		return "", nil, err
	}
	if source == "-" {
		source = "stdin"
	}
	variables, badLines := parseDotenv(text)
	if len(badLines) > 0 {
		// The line itself is not printed: it may hold a secret.
		return "", nil, fmt.Errorf("%s: line %d is not NAME=VALUE; nothing was imported", source, badLines[0])
	}
	if len(variables) == 0 {
		return "", nil, fmt.Errorf("%s has no NAME=VALUE lines", source)
	}
	for i := range variables {
		variables[i].Secret = !*plain
	}
	return *workspace, variables, nil
}

func workspaceEnvImport(args []string, stdin io.Reader) error {
	workspace, variables, err := parseWorkspaceEnvImport(args, stdin)
	if err != nil {
		return err
	}
	return changeWorkspaceEnv(workspace, protocol.WorkspaceEnvironmentSetParams{Set: variables})
}

func workspaceEnvScript(args []string, stdin io.Reader) error {
	fs, workspace := workspaceEnvFlags("workspace env script")
	file := fs.String("file", "", "set the setup script from this file (- for stdin)")
	clear := fs.Bool("clear", false, "remove the setup script")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || (*file != "" && *clear) {
		return errors.New(workspaceEnvScriptUsage)
	}
	var script *string
	if *file != "" {
		text, err := readFileOrStdin(*file, stdin)
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s is empty; use --clear to remove the setup script", *file)
		}
		script = &text
	}
	if *clear {
		script = new(string)
	}
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, *workspace, false)
		if err != nil {
			return err
		}
		var res protocol.WorkspaceEnvironmentResult
		if script == nil {
			if err = c.Call(protocol.MethodWorkspaceEnvironmentGet, protocol.WorkspaceEnvironmentGetParams{WorkspaceID: ws.ID}, &res); err != nil {
				return err
			}
			if res.SetupScript == "" {
				_, err = fmt.Fprintln(os.Stdout, "no setup script")
				return err
			}
			_, err = fmt.Fprintln(os.Stdout, strings.TrimSuffix(res.SetupScript, "\n"))
			return err
		}
		if err = c.Call(protocol.MethodWorkspaceEnvironmentSet, protocol.WorkspaceEnvironmentSetParams{WorkspaceID: ws.ID, SetupScript: script}, &res); err != nil {
			return err
		}
		state := "setup script set"
		if res.SetupScript == "" {
			state = "setup script removed"
		}
		_, err = fmt.Fprintf(os.Stdout, "workspace %s %s\n%s\n%s\n", ws.ID, ws.Name, state, workspaceEnvScope)
		return err
	})
}

func readFileOrStdin(path string, stdin io.Reader) (string, error) {
	if path == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func printWorkspaceVariables(w io.Writer, ws protocol.Workspace, res protocol.WorkspaceEnvironmentResult) error {
	if _, err := fmt.Fprintf(w, "workspace %s %s\n", ws.ID, ws.Name); err != nil {
		return err
	}
	if len(res.Variables) == 0 {
		_, err := fmt.Fprintln(w, "no variables")
		return err
	}
	for _, v := range res.Variables {
		line := v.Name + "=" + v.Value
		if v.Secret {
			line = v.Name + " (secret)"
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// parseDotenv reads NAME=VALUE lines: blank lines and # comments are skipped,
// a leading "export " is dropped, and a value may be wrapped in single quotes
// (kept as written) or double quotes (\n, \r, \t, \" and \\ are unescaped).
// A later line replaces an earlier one of the same name. badLines numbers
// the lines that are none of that. web/src/lib/dotenv.ts reads the same
// format for the dashboard.
func parseDotenv(text string) (variables []protocol.WorkspaceVariable, badLines []int) {
	index := map[string]int{}
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			badLines = append(badLines, i+1)
			continue
		}
		v := protocol.WorkspaceVariable{Name: name, Value: dotenvValue(strings.TrimSpace(value))}
		if at, seen := index[name]; seen {
			variables[at] = v
			continue
		}
		index[name] = len(variables)
		variables = append(variables, v)
	}
	return variables, badLines
}

var dotenvEscapes = strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t", `\"`, `"`, `\\`, `\`)

func dotenvValue(value string) string {
	if len(value) >= 2 {
		quote, last := value[0], value[len(value)-1]
		if quote == '\'' && last == '\'' {
			return value[1 : len(value)-1]
		}
		if quote == '"' && last == '"' {
			return dotenvEscapes.Replace(value[1 : len(value)-1])
		}
	}
	if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return value
}
