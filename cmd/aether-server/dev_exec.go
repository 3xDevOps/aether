package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/3xDevOps/Aether/internal/devexec"
)

// devExec is an internal entrypoint of the staged binary, including when its
// basename is aether-internal. It is not a public agent-selected run API.
func devExec(args []string) int {
	if len(args) < 2 {
		_, _ = fmt.Fprintln(os.Stderr, "dev-exec: expected run <key> <argv...> or control <key> <exec-id> <action> [grace-ms]")
		return 125
	}
	switch args[0] {
	case "run":
		code, err := devexec.Run(args[1], args[2:])
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "dev-exec:", err)
		}
		return code
	case "control":
		if len(args) < 4 || len(args) > 5 {
			return 125
		}
		request := devexec.Request{ExecID: args[2], Action: args[3]}
		if len(args) == 5 {
			grace, err := strconv.ParseInt(args[4], 10, 64)
			if err != nil {
				_, _ = fmt.Fprintln(os.Stderr, "dev-exec:", err)
				return 125
			}
			request.GraceMillis = grace
		}
		state, err := devexec.Control(context.Background(), args[1], request)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "dev-exec:", err)
			// Preserve a command-start failure's real exit code for callers.
			if state.ExecID != "" {
				_ = json.NewEncoder(os.Stdout).Encode(state)
			}
			return 125
		}
		if err := json.NewEncoder(os.Stdout).Encode(state); err != nil {
			return 125
		}
		return 0
	default:
		return 125
	}
}
