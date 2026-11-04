package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const closeUsage = "usage: aether close <session-id> --outcome merged|abandoned [--force]"

func init() {
	register(command{
		name:    "close",
		short:   "close a session as merged or abandoned",
		usage:   "aether close <session-id> --outcome <merged|abandoned> [--force]",
		example: "aether close run-d6ay38tj8j --outcome merged",
		flags:   true,
		run:     runClose,
	})
}

func runClose(args []string) error {
	fs := newFlagSet("close")
	outcome := fs.String("outcome", "", "merged or abandoned")
	force := fs.Bool("force", false, "on a terminal, close without asking even with close warnings")
	runID, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, closeUsage)
	}
	if runID == "" || (*outcome != "merged" && *outcome != "abandoned") {
		return errors.New(closeUsage)
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	return withControl(func(c *protocol.Client) error {
		return closeSession(c, runID, *outcome, *force, interactive, os.Stdin, os.Stdout, os.Stderr)
	})
}

func closeSession(c *protocol.Client, runID, outcome string, force, interactive bool, in io.Reader, out, errOut io.Writer) error {
	warnings, err := closeWarnings(c, runID, outcome)
	ask := len(warnings) > 0
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "Could not check what closing %s affects: %v\n", runID, err)
		// A server older than the CLI has no check; closing there worked
		// without one before.
		var perr *protocol.Error
		ask = !errors.As(err, &perr) || perr.Code != protocol.CodeMethodNotFound
	}
	if len(warnings) > 0 {
		_, _ = fmt.Fprintf(errOut, "Before closing %s:\n", runID)
		for _, w := range warnings {
			if w.Subsession != "" {
				_, _ = fmt.Fprintf(errOut, "  %s (subsession %s): %s\n", w.Kind, w.Subsession, w.Detail)
			} else {
				_, _ = fmt.Fprintf(errOut, "  %s: %s\n", w.Kind, w.Detail)
			}
			if w.URL != "" {
				_, _ = fmt.Fprintf(errOut, "    %s\n", w.URL)
			}
		}
	}
	if ask && interactive && !force {
		_, _ = fmt.Fprint(errOut, "Close anyway? [y/N]: ")
		if !yesAnswer(bufio.NewReader(in)) {
			return fmt.Errorf("aether close: %s was not closed", runID)
		}
	}
	var res protocol.RunResult
	if err = c.Call(protocol.MethodRunClose, protocol.RunCloseParams{RunID: runID, Outcome: outcome}, &res); err != nil {
		return runNotFound(runID, err)
	}
	_, _ = fmt.Fprintf(out, "session %s %s\n", res.Run.ID, res.Run.Status)
	return nil
}

func closeWarnings(c *protocol.Client, runID, outcome string) ([]protocol.CloseWarning, error) {
	var check protocol.RunCloseCheckResult
	if err := c.Call(protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: []string{runID}, Outcome: outcome}, &check); err != nil {
		return nil, olderServer(err)
	}
	if len(check.Runs) != 1 {
		return nil, fmt.Errorf("run.close_check answered for %d sessions, want 1", len(check.Runs))
	}
	if check.Runs[0].Error != nil {
		return nil, runNotFound(runID, check.Runs[0].Error)
	}
	return check.Runs[0].Warnings, nil
}
