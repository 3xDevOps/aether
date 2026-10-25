package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
	"golang.org/x/term"
)

const envUsage = "usage: aether env save|reset [--yes]"

func init() {
	register(command{
		name:  "env",
		short: "save or reset your Environment",
		run:   runEnv,
	})
}

func runEnv(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "save":
		return envSave()
	case len(args) == 1 && args[0] == "reset":
		return envReset(false, term.IsTerminal(int(os.Stdin.Fd())), os.Stdin, os.Stdout)
	case len(args) == 2 && args[0] == "reset" && args[1] == "--yes":
		return envReset(true, false, nil, os.Stdout)
	}
	return errors.New(envUsage)
}

func envSave() error {
	return withControl(func(c *protocol.Client) error {
		var result protocol.EnvSaveResult
		if err := c.Call(protocol.MethodEnvSave, struct{}{}, &result); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(os.Stdout, "saved %s\nnew runs and terminals start from this environment\n", result.Image); err != nil {
			return err
		}
		return nil
	})
}

func envReset(yes, interactive bool, in io.Reader, out io.Writer) error {
	if !yes {
		if !interactive {
			return errors.New("--yes is required because aether env reset destroys your Environment's container, unsaved changes and saved image: aether env reset --yes")
		}
		_, _ = fmt.Fprint(out, "This destroys your Environment's container, unsaved changes and saved image. Your home stays. Reset? [y/N]: ")
		if !yesAnswer(bufio.NewReader(in)) {
			return errors.New("aether env reset: nothing was reset")
		}
	}
	return withControl(func(c *protocol.Client) error {
		if err := c.Call(protocol.MethodEnvReset, struct{}{}, nil); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "environment reset to the standard image")
		return err
	})
}
