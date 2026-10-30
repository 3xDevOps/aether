package main

import (
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
)

// acpKeeper is an internal entrypoint of the staged binary, including when
// its basename is aether-internal: the keeper of an enhanced run's ACP
// adapter, and the bridge a server attaches to it through.
func acpKeeper(args []string) int {
	switch {
	case len(args) >= 4 && args[0] == "serve" && args[2] == "--":
		return acpkeeper.Serve(args[1], args[3:])
	case len(args) == 2 && args[0] == "attach":
		if err := acpkeeper.Bridge(args[1], os.Stdin, os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "acp-keeper:", err)
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintln(os.Stderr, "acp-keeper: expected serve <socket> -- <argv...> or attach <socket>")
	return 125
}
