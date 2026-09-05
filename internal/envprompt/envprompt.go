// Package envprompt renders the versioned prompt the local profile-scan
// engine hands to a coding agent.
package envprompt

import (
	_ "embed"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
)

// Version identifies the embedded template.
const Version = 5

//go:embed prompt.tmpl
var templateText string

var prompts = template.Must(template.New("envprompt").Parse(templateText))

// ProfileParams parameterizes a profile run: the agent is shown one
// rendered inventory of this machine's agent configuration directories and
// writes its import recommendation into OutputDir.
type ProfileParams struct {
	Inventory string
	OutputDir string
	RepoPath  string
}

type templateData struct {
	Inventory string
	OutputDir string
	RepoPath  string
}

// RenderProfile returns the full prompt for a profile run.
func RenderProfile(params ProfileParams) (string, error) {
	switch {
	case strings.TrimSpace(params.Inventory) == "":
		return "", errors.New("envprompt: profile prompt needs the harness inventory")
	case strings.TrimSpace(params.OutputDir) == "":
		return "", errors.New("envprompt: profile prompt needs an output directory")
	case !filepath.IsAbs(params.OutputDir):
		return "", fmt.Errorf("envprompt: profile prompt output directory must be absolute, got %q", params.OutputDir)
	}
	return render("profile", templateData{
		Inventory: params.Inventory,
		OutputDir: params.OutputDir,
		RepoPath:  params.RepoPath,
	})
}

func render(name string, data templateData) (string, error) {
	var out strings.Builder
	if err := prompts.ExecuteTemplate(&out, name, data); err != nil {
		return "", fmt.Errorf("envprompt: render %s prompt: %w", name, err)
	}
	return out.String(), nil
}
