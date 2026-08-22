package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/localops"
)

func init() {
	register(command{
		name:  "image",
		short: "image init scaffolding for Dockerfile and Dev Container",
		run:   runImage,
	})
}

func runImage(args []string) error {
	if len(args) == 0 || args[0] != "init" {
		return fmt.Errorf("usage: aether image init [directory] [--devcontainer] [--force]")
	}
	return imageInit(args[1:])
}

func imageInit(args []string) error {
	fs := flag.NewFlagSet("image init", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite existing scaffold files")
	devcontainer := fs.Bool("devcontainer", false, "also create .devcontainer/devcontainer.json")
	var directory string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		directory, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if directory == "" && fs.NArg() == 1 {
		directory = fs.Arg(0)
	}
	if directory == "" {
		directory = "."
	}
	if fs.NArg() > 1 {
		return errors.New("image init accepts at most one directory")
	}
	_, err := localops.ScaffoldFiles(directory, localops.ScaffoldOptions{Force: *force, DevContainer: *devcontainer})
	return err
}
