package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

var terminalTabName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func init() {
	register(command{
		name:  "terminal",
		short: "open or manage the member environment terminal",
		run:   runTerminal,
	})
}

func runTerminal(args []string) error {
	if len(args) > 0 && (args[0] == "status" || args[0] == "stop") {
		if len(args) != 1 {
			return terminalUsage()
		}
		switch args[0] {
		case "status":
			return terminalStatus()
		case "stop":
			return terminalStop()
		}
	}

	fs := flag.NewFlagSet("terminal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tab := fs.String("tab", "main", "terminal tab name")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return terminalUsage()
	}
	if !terminalTabName.MatchString(*tab) {
		return fmt.Errorf("invalid terminal tab %q: must match ^[a-z0-9-]{1,32}$", *tab)
	}

	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	cols, rows := termSize()
	stream, _, err := conn.TerminalStream(protocol.TerminalRequest{Tab: *tab, Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	return describeTerminalEnd(copyRaw(stream))
}

func terminalUsage() error {
	return errors.New("usage: aether terminal [--tab <name>] [status|stop]")
}

func terminalStatus() error {
	return withControl(func(c *protocol.Client) error {
		var status protocol.TerminalStatusResult
		if err := c.Call(protocol.MethodTerminalStatus, struct{}{}, &status); err != nil {
			return err
		}
		return printTerminalStatus(os.Stdout, status)
	})
}

func terminalStop() error {
	return withControl(func(c *protocol.Client) error {
		return c.Call(protocol.MethodTerminalStop, struct{}{}, nil)
	})
}

func printTerminalStatus(w io.Writer, status protocol.TerminalStatusResult) error {
	state := "stopped"
	if status.Running {
		state = "running"
	}
	if _, err := fmt.Fprintf(w, "status\t%s\n", state); err != nil {
		return err
	}
	if status.Image != "" {
		if _, err := fmt.Fprintf(w, "image\t%s\n", status.Image); err != nil {
			return err
		}
	}
	if status.SavedImage != "" {
		if _, err := fmt.Fprintf(w, "saved image\t%s\n", status.SavedImage); err != nil {
			return err
		}
	}
	if status.StartedAt != "" {
		if _, err := fmt.Fprintf(w, "started\t%s\n", status.StartedAt); err != nil {
			return err
		}
	}
	if len(status.Tabs) > 0 {
		if _, err := fmt.Fprintf(w, "tabs\t%s\n", strings.Join(status.Tabs, ", ")); err != nil {
			return err
		}
	}
	return nil
}

func describeTerminalEnd(err error) error {
	var exit *cli.RemoteExitError
	if !errors.As(err, &exit) {
		return err
	}
	if exit.Status == protocol.AttachExitMembershipRevoked {
		return errors.New("detached: your membership was removed or is pending approval again")
	}
	return err
}
