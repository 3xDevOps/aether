package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "env",
		short: "save or reset the member environment",
		run:   runEnv,
	})
}

func runEnv(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: aether env save|reset")
	}
	switch args[0] {
	case "save":
		return envSave()
	case "reset":
		return envReset()
	default:
		return errors.New("usage: aether env save|reset")
	}
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

func envReset() error {
	return withControl(func(c *protocol.Client) error {
		if err := c.Call(protocol.MethodEnvReset, struct{}{}, nil); err != nil {
			return err
		}
		_, err := fmt.Fprintln(os.Stdout, "environment reset to the standard image")
		return err
	})
}
