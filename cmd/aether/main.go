// Command aether is the Aether CLI: launches and steers runs, syncs git
// branches, and manages agent profiles against an aether-server.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/docs"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

func main() {
	selfupdate.RemoveOld()
	useResolvConfEnv()
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "aether:", err)
		if output := setupOutput(err); output != "" {
			fmt.Fprintf(os.Stderr, "setup script output:\n%s\n", strings.TrimRight(output, "\n"))
		}
		code := 1
		var status *exitStatusError
		if errors.As(err, &status) {
			code = status.code
		}
		os.Exit(code)
	}
}

type exitStatusError struct {
	code int
	err  error
}

func (e *exitStatusError) Error() string {
	return e.err.Error()
}

func (e *exitStatusError) Unwrap() error {
	return e.err
}

func init() {
	register(command{
		name:    "version",
		short:   "print the version",
		usage:   "aether version",
		example: "aether version",
		run: func([]string) error {
			_, err := fmt.Println("aether", version.String())
			return err
		},
	})
}

func dispatch(args []string) error {
	helpFlags = nil
	if len(args) == 0 || isHelpFlag(args[0]) {
		printHelp(os.Stdout)
		return nil
	}
	if args[0] == "help" {
		return helpCmd(args[1:])
	}
	i := slices.IndexFunc(commands, func(c command) bool { return c.name == args[0] })
	if i < 0 {
		printHelp(os.Stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
	c := commands[i]
	if path, ok := helpPath(c, args[1:]); ok && !runsForHelp(c, path) {
		return writeHelp(os.Stdout, c, path, nil)
	}
	err := c.run(args[1:])
	switch {
	case helpFlags == nil:
		return err
	case errors.Is(err, flag.ErrHelp), slices.ContainsFunc(args[1:], isHelpFlag):
		return writeHelp(os.Stdout, c, helpFlags.Name(), helpFlags)
	case err != nil:
		return fmt.Errorf("%w; see aether %s --help", err, helpFlags.Name())
	}
	return nil
}

func runsForHelp(c command, path string) bool {
	if path == c.name {
		return c.flags
	}
	return slices.Contains(flagSubcommands, path)
}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}

func helpCmd(args []string) error {
	if len(args) == 1 && args[0] == "agents" {
		guide, err := docs.FS.ReadFile("agents.md")
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(guide)
		return err
	}
	return dispatch(append(args, "--help"))
}

// Only a help flag right after the command's literal words asks for help:
// a later one can belong to a run, as in aether ssh <run-id> ls --help.
func helpPath(c command, args []string) (string, bool) {
	path := c.name
	for _, arg := range args {
		if isHelpFlag(arg) {
			return path, true
		}
		if len(linesFor(c.usage, path+" "+arg)) == 0 {
			return "", false
		}
		path += " " + arg
	}
	return "", false
}

func linesFor(text, path string) []string {
	var lines []string
	match := false
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, " ") {
			rest, ok := strings.CutPrefix(line, "aether "+path)
			match = ok && (rest == "" || rest[0] == ' ')
		}
		if match {
			lines = append(lines, line)
		}
	}
	return lines
}

func writeHelp(w io.Writer, c command, path string, flags *flag.FlagSet) error {
	var b strings.Builder
	lines := linesFor(c.usage, path)
	if len(lines) == 0 {
		lines = linesFor(c.usage, c.name)
	}
	prefix := "usage: "
	for _, line := range lines {
		b.WriteString(prefix + line + "\n")
		prefix = "       "
	}
	if path == c.name {
		b.WriteString("\n" + c.short + "\n")
	}
	if flags != nil {
		var defaults strings.Builder
		flags.SetOutput(&defaults)
		flags.PrintDefaults()
		if defaults.Len() > 0 {
			b.WriteString("\nflags:\n" + defaults.String())
		}
	}
	if examples := linesFor(c.example, path); len(examples) > 0 {
		b.WriteString("\nexample:\n  " + strings.Join(examples, "\n  ") + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func parseLeadingArg(fs *flag.FlagSet, args []string) (string, error) {
	var leading string
	// A lone "-" is a positional argument (stdin), not a flag.
	if len(args) > 0 && (args[0] == "-" || !strings.HasPrefix(args[0], "-")) {
		leading, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if leading != "" && fs.NArg() == 0 {
		return leading, nil
	}
	if leading == "" && fs.NArg() == 1 {
		return fs.Arg(0), nil
	}
	return "", fmt.Errorf("expected exactly one positional argument")
}

func printHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: aether <command> [args]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "commands:")
	sorted := slices.SortedFunc(slices.Values(commands), func(a, b command) int { return strings.Compare(a.name, b.name) })
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range sorted {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", c.name, c.short)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "start: aether status shows what is set up and the next step; aether <command> --help explains a command")
	_, _ = fmt.Fprintln(w, "coding agents: aether help agents, or https://github.com/3xDevOps/Aether/blob/main/docs/agents.md")
}
