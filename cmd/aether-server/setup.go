package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/serversetup"
	"golang.org/x/term"
)

func setup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	configPath := fs.String("config", serversetup.DefaultConfigPath, "options file to write")
	unitPath := fs.String("unit", serversetup.UnitPath, "systemd unit file to write")
	force := fs.Bool("force", false, "overwrite an existing config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("setup asks questions and needs a terminal; use `aether-server install` with flags instead")
	}
	// Refuse before the questions rather than after them: nobody should
	// answer six prompts only to be told the write needs sudo.
	if err := requireRoot(); err != nil {
		return err
	}
	values, err := askServerOptions(os.Stdout, os.Stdin, *configPath)
	if err != nil {
		return err
	}
	if values == nil {
		_, _ = fmt.Fprintln(os.Stdout, "nothing written")
		return nil
	}
	return writeAndReport(os.Stdout, *unitPath, *configPath, values, *force)
}

// askServerOptions walks the operator through the handful of options a
// server install actually chooses, seeding each default from the existing
// config file so re-running setup is not destructive. It returns a nil map
// when the operator declines the summary.
func askServerOptions(w io.Writer, in io.Reader, configPath string) (map[string]string, error) {
	current, err := serversetup.Load(configPath)
	if err != nil {
		return nil, err
	}
	p := &prompter{w: w, lines: bufio.NewReader(in), options: serveFlagSet()}
	// An existing config answers first, then the packaged service posture,
	// then the flag's own default, so re-running setup is not destructive
	// and a first run offers what a service install would have used.
	service := serversetup.ServiceDefaults()
	def := func(key string) string {
		if v, ok := current[key]; ok {
			return v
		}
		if v, ok := service[key]; ok {
			return v
		}
		return p.options.Lookup(key).DefValue
	}

	values := map[string]string{}
	values["addr"] = p.ask("addr", "SSH listen address", def("addr"))
	values["data-dir"] = p.ask("data-dir", "Data directory", def("data-dir"))

	_, _ = fmt.Fprintln(w, "\nWith tailnet auto-join on, anyone already on your tailnet becomes an")
	_, _ = fmt.Fprintln(w, "approved member on first connect, with no admin approving them.")
	values["tailnet-auto-join"] = p.ask("tailnet-auto-join",
		"Auto-approve tailnet identities (true/false)", def("tailnet-auto-join"))

	_, _ = fmt.Fprintln(w, "\nRequiring a pubkey on top of tailnet identity means a member must have")
	_, _ = fmt.Fprintln(w, "linked a key before their tailnet identity is trusted.")
	values["tailnet-require-key"] = p.ask("tailnet-require-key",
		"Also require pubkey verification on tailnet connections (true/false)", def("tailnet-require-key"))

	if p.err != nil {
		return nil, p.err
	}
	_, _ = fmt.Fprintf(w, "\n%s will hold:\n\n%s\n", configPath, serversetup.Render(values))
	if !p.confirm("Write it", true) || p.err != nil {
		return nil, p.err
	}
	return values, nil
}

// prompter reads answers off one line-oriented stream, latching the first
// read error so a caller checks it once at the end instead of after every
// question.
type prompter struct {
	w       io.Writer
	lines   *bufio.Reader
	options *flag.FlagSet
	err     error
	// eof stops ask from re-asking once the input is exhausted, which
	// would otherwise spin forever on a default the options reject.
	eof bool
}

// ask shows def, returns it when the operator just presses enter, and
// re-asks when the answer is not a value the named option accepts.
func (p *prompter) ask(option, label, def string) string {
	for p.err == nil && !p.eof {
		answer := p.readLine(label, def)
		if answer == "" || p.err != nil || p.eof {
			return answer
		}
		if err := p.options.Set(option, answer); err != nil {
			_, _ = fmt.Fprintf(p.w, "  %v; try again\n", err)
			continue
		}
		return answer
	}
	return def
}

// confirm asks a yes/no question, showing def as the word an empty answer
// takes.
func (p *prompter) confirm(label string, def bool) bool {
	word := "no"
	if def {
		word = "yes"
	}
	switch strings.ToLower(p.readLine(label+" (yes/no)", word)) {
	case "y", "yes", "true":
		return true
	case "n", "no", "false":
		return false
	default:
		return def
	}
}

func (p *prompter) readLine(label, def string) string {
	if p.err != nil || p.eof {
		return def
	}
	_, _ = fmt.Fprintf(p.w, "%s [%s]: ", label, def)
	line, err := p.lines.ReadString('\n')
	switch {
	case err == io.EOF:
		p.eof = true
	case err != nil:
		p.err = err
		return def
	}
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}
