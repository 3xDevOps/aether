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
	force := fs.Bool("force", false, "close without asking, even when the close affects others")
	runID, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, closeUsage)
	}
	if runID == "" || (*outcome != "merged" && *outcome != "abandoned") {
		return errors.New(closeUsage)
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	return withControl(func(c *protocol.Client) error {
		return closeSession(c, runID, *outcome, *force, interactive, os.Stdin, os.Stdout)
	})
}

// closeSession prints what closing runID would affect and, unless force,
// asks before it closes.
func closeSession(c *protocol.Client, runID, outcome string, force, interactive bool, in io.Reader, out io.Writer) error {
	var check protocol.RunCloseCheckResult
	if err := c.Call(protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: []string{runID}}, &check); err != nil {
		return err
	}
	if len(check.Runs) != 1 {
		return fmt.Errorf("run.close_check answered for %d sessions, want 1", len(check.Runs))
	}
	if check.Runs[0].Error != nil {
		return runNotFound(runID, check.Runs[0].Error)
	}
	if warnings := check.Runs[0].Warnings; len(warnings) > 0 {
		_, _ = fmt.Fprintf(out, "Closing %s affects others:\n", runID)
		for _, w := range warnings {
			_, _ = fmt.Fprintf(out, "  %s: %s\n", w.Kind, w.Detail)
			if w.URL != "" {
				_, _ = fmt.Fprintf(out, "    %s\n", w.URL)
			}
		}
		if !force {
			if !interactive {
				return fmt.Errorf("no terminal to ask on; to close anyway: aether close %s --outcome %s --force", runID, outcome)
			}
			_, _ = fmt.Fprint(out, "Close anyway? [y/N]: ")
			if !yesAnswer(bufio.NewReader(in)) {
				return fmt.Errorf("aether close: %s was not closed", runID)
			}
		}
	}
	var res protocol.RunResult
	if err := c.Call(protocol.MethodRunClose, protocol.RunCloseParams{RunID: runID, Outcome: outcome}, &res); err != nil {
		return runNotFound(runID, err)
	}
	_, _ = fmt.Fprintf(out, "session %s %s\n", res.Run.ID, res.Run.Status)
	return nil
}
