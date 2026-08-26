package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/serversetup"
)

func configCmd(args []string) error {
	// The subcommand comes off the front before flags are parsed: Go's
	// flag package stops at the first non-flag argument, so parsing first
	// would silently drop a --config that follows "show" or "set".
	if len(args) == 0 {
		return errors.New("usage: aether-server config <show|path|set|edit> [--config <file>]")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	path := fs.String("config", serversetup.DefaultConfigPath, "options file to read or write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	switch sub {
	case "show":
		return configShow(os.Stdout, *path)
	case "path":
		fmt.Println(*path)
		return nil
	case "set":
		if len(rest) != 2 {
			return errors.New("usage: aether-server config set <key> <value>")
		}
		return configSet(os.Stdout, *path, rest[0], rest[1])
	case "edit":
		return configEdit(os.Stdout, *path)
	default:
		return fmt.Errorf("unknown config command %q (want show, path, set, or edit)", sub)
	}
}

// configShow prints every server option with the value the next `serve`
// would use, marking which ones the config file is responsible for.
func configShow(w io.Writer, path string) error {
	values, err := serversetup.Load(path)
	if err != nil {
		return err
	}
	fs := serveFlagSet()
	retired, err := serversetup.Apply(fs, values)
	if err != nil {
		return err
	}
	for _, key := range retired {
		_, _ = fmt.Fprintf(w, "note: %q is no longer an option and is ignored; drop the line with `aether-server config edit`\n", key)
	}
	if len(values) == 0 {
		_, _ = fmt.Fprintf(w, "%s (no config file; every option is at its default)\n\n", path)
	} else {
		_, _ = fmt.Fprintf(w, "%s\n\n", path)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "OPTION\tVALUE\tSOURCE")
	fs.VisitAll(func(f *flag.Flag) {
		source := "default"
		if _, ok := values[f.Name]; ok {
			source = "config"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", f.Name, f.Value.String(), source)
	})
	return tw.Flush()
}

// configSet rewrites one key, leaving every other key in the file as it is.
// The key and value are validated against the real `serve` options, so the
// file can never hold something the server would reject at startup.
func configSet(w io.Writer, path, key, value string) error {
	fs := serveFlagSet()
	if fs.Lookup(key) == nil {
		return fmt.Errorf("unknown option %q (known: %s)", key, joinOptions())
	}
	if err := fs.Set(key, value); err != nil {
		return fmt.Errorf("invalid value for %q: %w", key, err)
	}
	values, err := serversetup.Load(path)
	if err != nil {
		return err
	}
	values[key] = value
	if err := serversetup.WriteConfig(path, values); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "%s: %s = %s\n", path, key, value)
	_, _ = fmt.Fprintf(w, "apply it with:\n  %s\n", serversetup.RestartCommand)
	return nil
}

// configEdit opens the config file in the operator's editor, then reparses
// it so a syntax error or a bad key surfaces now rather than at the next
// restart.
func configEdit(w io.Writer, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := serversetup.WriteConfig(path, map[string]string{}); err != nil {
			return err
		}
	}
	// EDITOR commonly carries arguments ("code --wait", "emacs -nw"), so
	// it is split the way cmd/aether/agent.go splits its own command
	// strings. exec.Command never invokes a shell, so a value with shell
	// metacharacters is looked up as a literal name and fails safely.
	editor := strings.Fields(os.Getenv("EDITOR"))
	if len(editor) == 0 {
		fallback := "/usr/bin/editor"
		if _, err := os.Stat(fallback); err != nil {
			fallback = "vi"
		}
		editor = []string{fallback}
	}
	cmd := exec.Command(editor[0], append(editor[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", strings.Join(editor, " "), path, err)
	}
	values, err := serversetup.Load(path)
	if err != nil {
		return err
	}
	retired, err := serversetup.Apply(serveFlagSet(), values)
	if err != nil {
		return err
	}
	for _, key := range retired {
		_, _ = fmt.Fprintf(w, "note: %q is no longer an option and is ignored\n", key)
	}
	_, _ = fmt.Fprintf(w, "apply it with:\n  %s\n", serversetup.RestartCommand)
	return nil
}

// serveFlagSet builds the `serve` options for validation only, with output
// discarded so a bad value returns an error instead of printing usage.
func serveFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	serveFlags(fs)
	return fs
}

// joinOptions lists every known option name, for the error a typo produces
// and for the usage text.
func joinOptions() string {
	var names []string
	serveFlagSet().VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	slices.Sort(names)
	return strings.Join(names, ", ")
}
