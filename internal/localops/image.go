package localops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// ErrScaffoldExists marks a scaffold refusal: a target file already
// exists and Force is off. Callers branch on it to answer "already
// there" differently from an I/O failure.
var ErrScaffoldExists = errors.New("scaffold file already exists")

// existsError renders the exact CLI refusal message while unwrapping to
// ErrScaffoldExists for programmatic callers.
type existsError struct {
	path string
}

func (e *existsError) Error() string {
	return fmt.Sprintf("refusing to overwrite existing %s; use --force", e.path)
}

func (e *existsError) Unwrap() error { return ErrScaffoldExists }

// ScaffoldOptions mirrors the `aether image init` flags.
type ScaffoldOptions struct {
	// Force overwrites existing scaffold files.
	Force bool
	// DevContainer also writes .devcontainer/devcontainer.json.
	DevContainer bool
}

// ScaffoldFiles writes the neutral image scaffold into directory and
// returns the paths written. Without Force, any pre-existing target file
// refuses the whole scaffold (wrapping ErrScaffoldExists) before anything
// is written.
func ScaffoldFiles(directory string, options ScaffoldOptions) ([]string, error) {
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
				return nil, &existsError{path: path}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("check %s: %w", path, err)
			}
		}
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create image scaffold directory: %w", err)
	}
	if options.DevContainer {
		if err := os.MkdirAll(filepath.Join(directory, ".devcontainer"), 0o755); err != nil {
			return nil, fmt.Errorf("create devcontainer directory: %w", err)
		}
	}
	for i, path := range targets {
		if err := os.WriteFile(path, []byte(contents[i]), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}
	return targets, nil
}

// Scaffold is the /local/v1 image.scaffold core: kind "dockerfile" writes
// the Dockerfile and .dockerignore, kind "devcontainer" adds the Dev
// Container definition. Existing files are never overwritten.
func Scaffold(repo, kind string) ([]string, error) {
	var options ScaffoldOptions
	switch kind {
	case "dockerfile":
	case "devcontainer":
		options.DevContainer = true
	default:
		return nil, fmt.Errorf("unknown scaffold kind %q (want dockerfile or devcontainer)", kind)
	}
	return ScaffoldFiles(repo, options)
}
