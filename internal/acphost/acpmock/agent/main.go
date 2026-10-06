// Command agent is the acpmock agent on stdio, for integration tests that
// run it inside a container in place of a real ACP adapter. Its one
// argument names the fixture: claude (the default) or codex.
package main

import (
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
)

func main() {
	name := "claude"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	fix, err := acpmock.Load(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	acpmock.New(fix).Serve(os.Stdin, os.Stdout)
}
