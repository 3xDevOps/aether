package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	workspaceEnvUsage       = "usage: aether workspace env <list|set|unset|import|script|file|egress>"
	workspaceEnvListUsage   = "usage: aether workspace env list [--workspace <name-or-id>]"
	workspaceEnvSetUsage    = "usage: aether workspace env set [--workspace <name-or-id>] <NAME>=<value> [<NAME>=<value> ...]\n   or: aether workspace env set [--workspace <name-or-id>] --secret <NAME>   (the value is read from stdin)"
	workspaceEnvUnsetUsage  = "usage: aether workspace env unset [--workspace <name-or-id>] <NAME> [<NAME> ...]"
	workspaceEnvImportUsage = "usage: aether workspace env import [--workspace <name-or-id>] [--plain] <file|->"
	workspaceEnvScriptUsage = "usage: aether workspace env script [--workspace <name-or-id>] [--file <path|->|--clear]"

	workspaceEnvFileUsage    = "usage: aether workspace env file <put|rm>"
	workspaceEnvFilePutUsage = "usage: aether workspace env file put [--workspace <name-or-id>] <local file> [--path <repo path>]"
	workspaceEnvFileRmUsage  = "usage: aether workspace env file rm [--workspace <name-or-id>] <repo path>"

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
	case "file":
		return workspaceEnvFile(args[1:])
	case "egress":
		return workspaceEnvEgress(args[1:])
	default:
		return fmt.Errorf("unknown workspace env command %q", args[0])
	}
}

func workspaceEnvFlags(name string) (*flag.FlagSet, *string) {
	fs := newFlagSet(name)
	return fs, fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
}

func workspaceEnvList(args []string) error {
	fs, workspace := workspaceEnvFlags("workspace env list")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, workspaceEnvListUsage)
	}
	if fs.NArg() != 0 {
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
	// A server from before workspace files ignores them and answers without
	// the list.
	if (len(change.Files) > 0 || len(change.RemoveFiles) > 0) && res.Files == nil {
		return errors.New("this server has no workspace files; update aether-server, then run the command again")
	}
	if err := printWorkspaceVariables(os.Stdout, ws, res); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(os.Stdout, workspaceEnvScope); err != nil {
		return err
	}
	return printMirrorWarning(os.Stdout, res.Warning)
}

func parseWorkspaceEnvSet(args []string) (workspace string, plain []protocol.WorkspaceVariable, secretName string, err error) {
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
		plain = append(plain, protocol.WorkspaceVariable{Name: name, Value: value})
	}
	return *named, plain, "", nil
}

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
	// A shell pipeline ends the value with a newline that is not part of it.
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
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, workspaceEnvUnsetUsage)
	}
	if fs.NArg() == 0 {
		return errors.New(workspaceEnvUnsetUsage)
	}
	return changeWorkspaceEnv(*workspace, protocol.WorkspaceEnvironmentSetParams{Unset: fs.Args()})
}

func parseWorkspaceEnvImport(args []string, stdin io.Reader) (string, []protocol.WorkspaceVariable, error) {
	fs, workspace := workspaceEnvFlags("workspace env import")
	plain := fs.Bool("plain", false, "store the values as plain variables instead of secrets")
	if err := fs.Parse(args); err != nil {
		return "", nil, fmt.Errorf("%w\n%s", err, workspaceEnvImportUsage)
	}
	if fs.NArg() != 1 {
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
		// An empty value has nothing to hide, and a secret is listed without
		// its value, which would hide that it is empty.
		variables[i].Secret = !*plain && variables[i].Value != ""
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

func workspaceEnvFile(args []string) error {
	if len(args) < 1 {
		return errors.New(workspaceEnvFileUsage)
	}
	switch args[0] {
	case "put":
		workspace, file, err := parseWorkspaceEnvFilePut(args[1:])
		if err != nil {
			return err
		}
		return changeWorkspaceEnv(workspace, protocol.WorkspaceEnvironmentSetParams{Files: []protocol.WorkspaceFile{file}})
	case "rm":
		fs, workspace := workspaceEnvFlags("workspace env file rm")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("%w\n%s", err, workspaceEnvFileRmUsage)
		}
		if fs.NArg() != 1 {
			return errors.New(workspaceEnvFileRmUsage)
		}
		return changeWorkspaceEnv(*workspace, protocol.WorkspaceEnvironmentSetParams{RemoveFiles: fs.Args()})
	default:
		return fmt.Errorf("unknown workspace env file command %q", args[0])
	}
}

func parseWorkspaceEnvFilePut(args []string) (string, protocol.WorkspaceFile, error) {
	fs, workspace := workspaceEnvFlags("workspace env file put")
	repoPath := fs.String("path", "", "the file's path in the repository (default: <local file>, when it is a clean relative path)")
	if err := fs.Parse(args); err != nil {
		return "", protocol.WorkspaceFile{}, fmt.Errorf("%w\n%s", err, workspaceEnvFilePutUsage)
	}
	if fs.NArg() == 0 {
		return "", protocol.WorkspaceFile{}, errors.New(workspaceEnvFilePutUsage)
	}
	// flag stops at the first argument that is not one, so --path after the
	// local file needs a second Parse.
	local := fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return "", protocol.WorkspaceFile{}, fmt.Errorf("%w\n%s", err, workspaceEnvFilePutUsage)
	}
	if fs.NArg() != 0 {
		return "", protocol.WorkspaceFile{}, errors.New(workspaceEnvFilePutUsage)
	}
	if *repoPath == "" {
		rel := filepath.ToSlash(local)
		if !filepath.IsLocal(local) || path.Clean(rel) != rel {
			return "", protocol.WorkspaceFile{}, fmt.Errorf("%s is not a clean relative path; name the file's path in the repository with --path, such as --path .env", local)
		}
		*repoPath = rel
	}
	file, err := os.Open(local)
	if err != nil {
		return "", protocol.WorkspaceFile{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, protocol.MaxWorkspaceVariableValueBytes+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", protocol.WorkspaceFile{}, fmt.Errorf("read %s: %w", local, err)
	}
	if len(data) > protocol.MaxWorkspaceVariableValueBytes {
		return "", protocol.WorkspaceFile{}, fmt.Errorf("%s is larger than %d bytes, the most a workspace file holds", local, protocol.MaxWorkspaceVariableValueBytes)
	}
	// JSON would carry other bytes as U+FFFD, changing the file.
	if !utf8.Valid(data) {
		return "", protocol.WorkspaceFile{}, fmt.Errorf("%s is not UTF-8 text; a workspace file must be", local)
	}
	return *workspace, protocol.WorkspaceFile{Path: *repoPath, Content: string(data)}, nil
}

func workspaceEnvScript(args []string, stdin io.Reader) error {
	fs, workspace := workspaceEnvFlags("workspace env script")
	file := fs.String("file", "", "set the setup script from this file (- for stdin)")
	clear := fs.Bool("clear", false, "remove the setup script")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, workspaceEnvScriptUsage)
	}
	if fs.NArg() != 0 || (*file != "" && *clear) {
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
		if _, err = fmt.Fprintf(os.Stdout, "workspace %s %s\n%s\n%s\n", ws.ID, ws.Name, state, workspaceEnvScope); err != nil {
			return err
		}
		return printMirrorWarning(os.Stdout, res.Warning)
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
	lines := make([]string, 0, len(res.Variables)+len(res.Files))
	for _, v := range res.Variables {
		line := v.Name + "=" + v.Value
		if v.Secret {
			line = v.Name + " (secret)"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, "no variables")
	}
	for _, f := range res.Files {
		lines = append(lines, fmt.Sprintf("file %s (%d bytes)", f.Path, f.Size))
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// web/src/lib/dotenv.ts reads the same format for the dashboard: a change to
// one is a change to both.
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
	if value != "" && (value[0] == '"' || value[0] == '\'') {
		if end := closingQuote(value); end > 0 {
			if rest := strings.TrimSpace(value[end+1:]); rest == "" || rest[0] == '#' {
				if value[0] == '"' {
					return dotenvEscapes.Replace(value[1:end])
				}
				return value[1:end]
			}
		}
	}
	if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return value
}

func closingQuote(value string) int {
	for i := 1; i < len(value); i++ {
		switch {
		case value[0] == '"' && value[i] == '\\':
			i++
		case value[i] == value[0]:
			return i
		}
	}
	return -1
}
