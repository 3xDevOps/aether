package main

import (
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "pause",
		short:   "pause a run",
		usage:   "aether pause <run-id>",
		example: "aether pause run-d6ay38tj8j",
		run:     runPause,
	})
}

func runPause(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether pause <run-id>")
	}
	return withControl(func(c *protocol.Client) error {
		return actOnRun(c, os.Stdout, protocol.MethodRunPause, args[0])
	})
}

func actOnRun(c *protocol.Client, w io.Writer, method, runID string) error {
	if err := c.Call(method, protocol.RunIDParams{RunID: runID}, nil); err != nil {
		return runNotFound(runID, err)
	}
	r, err := getRun(c, runID)
	if err != nil {
		return fmt.Errorf("%s run %s succeeded; reading its status failed: %w", method, runID, err)
	}
	status := r.Status
	if r.Paused {
		status = "paused"
	}
	_, err = fmt.Fprintf(w, "run %s %s\n", r.ID, status)
	return err
}
