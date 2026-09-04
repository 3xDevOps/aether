// Package envprompt renders the versioned prompt the local inventory
// engine hands to a coding agent. The template ships embedded in the
// binary so every install of a given release sends the same instructions;
// Version rises with any template change so stored results can name the
// prompt that produced them.
package envprompt

import (
	_ "embed"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/3xDevOps/Aether/internal/envdef"
)

// Version identifies the embedded template. Bump it on any change to
// prompt.tmpl; the package test pins the template's hash to this number,
// so an edit without a bump fails the build gate.
const Version = 4

//go:embed prompt.tmpl
var templateText string

var prompts = template.Must(template.New("envprompt").Parse(templateText))

// InventoryParams parameterizes the first-run prompt.
type InventoryParams struct {
	// BaseImage is the image the generated Dockerfile must build from.
	BaseImage string
}

// RepoParams parameterizes a from-repo run: the agent reads the
// repository it is started in and writes the pair into OutputDir.
type RepoParams struct {
	// BaseImage is the image the generated Dockerfile must build from.
	BaseImage string
	// OutputDir is the absolute path of the scratch directory the two
	// output files must land in; the agent runs inside the repository,
	// so a relative path would point into it.
	OutputDir string
}

// RefineParams parameterizes a follow-up run: the pair a previous run
// produced plus the feedback to apply, all embedded verbatim.
type RefineParams struct {
	Dockerfile   string
	ManifestJSON string
	Feedback     string
	// OutputDir, when set, anchors the refine run in a repository: the
	// agent runs with the repository as its working directory, may read
	// its files but never change them, and writes the revised pair into
	// this absolute scratch path instead of the current directory. Empty
	// for machine-mirror refines.
	OutputDir string
}

// ProfileParams parameterizes a profile run: the agent is shown one
// rendered inventory of this machine's agent configuration directories
// and writes its import recommendation into OutputDir.
type ProfileParams struct {
	// Inventory is the per-harness inventory block, embedded verbatim. It
	// carries names, paths, counts and sizes only - never file contents,
	// and never the excluded credential files.
	Inventory string
	// OutputDir is the absolute path of the scratch directory profile.json
	// must land in. It is named even when no repository is given, so a
	// repo-anchored run is never pointed at its working directory.
	OutputDir string
	// RepoPath, when set, anchors the run in a repository: the agent runs
	// there, may read its files but never change them, and prefers
	// configuration that matches the project. Empty means the agent
	// reasons from the inventories alone.
	RepoPath string
}

// templateData is the single shape all templates render from.
type templateData struct {
	BaseImage    string
	OutputDir    string
	Dockerfile   string
	ManifestJSON string
	Feedback     string
	Inventory    string
	RepoPath     string
}

// RenderInventory returns the full prompt for an inventory run.
func RenderInventory(params InventoryParams) (string, error) {
	if strings.TrimSpace(params.BaseImage) == "" {
		return "", errors.New("envprompt: inventory prompt needs a base image")
	}
	return render("inventory", templateData{BaseImage: params.BaseImage})
}

// RenderRepo returns the full prompt for a from-repo run.
func RenderRepo(params RepoParams) (string, error) {
	switch {
	case strings.TrimSpace(params.BaseImage) == "":
		return "", errors.New("envprompt: repo prompt needs a base image")
	case strings.TrimSpace(params.OutputDir) == "":
		return "", errors.New("envprompt: repo prompt needs an output directory")
	case !filepath.IsAbs(params.OutputDir):
		return "", fmt.Errorf("envprompt: repo prompt output directory must be absolute, got %q", params.OutputDir)
	}
	return render("repo", templateData{
		BaseImage: params.BaseImage,
		OutputDir: params.OutputDir,
	})
}

// RenderRefine returns the full prompt for a refine run. The contract
// section names the same base image the inventory contract enforces.
func RenderRefine(params RefineParams) (string, error) {
	switch {
	case strings.TrimSpace(params.Dockerfile) == "":
		return "", errors.New("envprompt: refine prompt needs the previous Dockerfile")
	case strings.TrimSpace(params.ManifestJSON) == "":
		return "", errors.New("envprompt: refine prompt needs the previous manifest JSON")
	case strings.TrimSpace(params.Feedback) == "":
		return "", errors.New("envprompt: refine prompt needs feedback text")
	case params.OutputDir != "" && !filepath.IsAbs(params.OutputDir):
		return "", fmt.Errorf("envprompt: refine prompt output directory must be absolute, got %q", params.OutputDir)
	}
	return render("refine", templateData{
		BaseImage:    envdef.BaseImage,
		Dockerfile:   params.Dockerfile,
		ManifestJSON: params.ManifestJSON,
		Feedback:     params.Feedback,
		OutputDir:    params.OutputDir,
	})
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
