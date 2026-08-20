package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const neutralScaffoldDockerfile = `FROM ubuntu:24.04

WORKDIR /workspace
CMD ["/bin/bash"]
`

const neutralScaffoldDockerignore = `.git
.gitignore
Dockerfile*
.dockerignore
.devcontainer/
`

const neutralScaffoldDevcontainer = `{
  "name": "Aether workspace",
  "build": {
    "dockerfile": "../Dockerfile",
    "context": ".."
  },
  "workspaceFolder": "/workspace"
}
`

type imageScaffoldOptions struct {
	Force        bool
	DevContainer bool
}

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
	return initImageScaffold(directory, imageScaffoldOptions{Force: *force, DevContainer: *devcontainer})
}

func initImageScaffold(directory string, options imageScaffoldOptions) error {
	targets := []string{
		filepath.Join(directory, "Dockerfile"),
		filepath.Join(directory, ".dockerignore"),
	}
	contents := []string{neutralScaffoldDockerfile, neutralScaffoldDockerignore}
	if options.DevContainer {
		targets = append(targets, filepath.Join(directory, ".devcontainer", "devcontainer.json"))
		contents = append(contents, neutralScaffoldDevcontainer)
	}
	if !options.Force {
		for _, path := range targets {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("refusing to overwrite existing %s; use --force", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("check %s: %w", path, err)
			}
		}
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create image scaffold directory: %w", err)
	}
	if options.DevContainer {
		if err := os.MkdirAll(filepath.Join(directory, ".devcontainer"), 0o755); err != nil {
			return fmt.Errorf("create devcontainer directory: %w", err)
		}
	}
	for i, path := range targets {
		if err := os.WriteFile(path, []byte(contents[i]), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
