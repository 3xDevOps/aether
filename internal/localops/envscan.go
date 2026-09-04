package localops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/envdef"
	"github.com/3xDevOps/Aether/internal/envprompt"
	"github.com/3xDevOps/Aether/internal/harness"
)

// Scan modes. Inventory is a first run against the machine; repo derives
// the environment from a repository's own files instead; refine reruns
// the agent over a previous Dockerfile and manifest pair with feedback;
// profile asks which of this machine's agent configurations are worth
// importing and produces a recommendation instead of an image pair.
const (
	ScanModeInventory = "inventory"
	ScanModeRepo      = "repo"
	ScanModeRefine    = "refine"
	ScanModeProfile   = "profile"
)

// HarnessStatus is one setup-capable harness's local availability.
type HarnessStatus struct {
	Name string `json:"name"`
	// Installed reports whether the harness executable is on PATH.
	Installed bool `json:"installed"`
}

// DetectHarnesses reports, for each setup-capable harness in setup order,
// whether its executable is on this machine's PATH. Plain PATH lookup, no
// agent involved.
func DetectHarnesses() []HarnessStatus {
	profiles := harness.SetupHarnesses()
	out := make([]HarnessStatus, 0, len(profiles))
	for _, p := range profiles {
		_, err := exec.LookPath(p.HeadlessArgs[0])
		out = append(out, HarnessStatus{Name: p.Name, Installed: err == nil})
	}
	return out
}

// ScanOptions parameterizes one RunScan call.
type ScanOptions struct {
	// Harness names the setup-capable harness to run, or "fake" for a
	// canned pair that exercises the flow without a vendor CLI.
	Harness string
	// Mode is ScanModeInventory, ScanModeRepo, or ScanModeRefine.
	Mode string
	// RepoPath is the repository folder a repo scan reads. Required when
	// Mode is ScanModeRepo; set on a refine run when the pair being
	// refined came from a repo scan, so the agent can read the repository
	// again. The agent runs with the repository as its working directory
	// but writes its output into the scratch directory, and the scan
	// fails if the repository's git status changes during the run.
	RepoPath string
	// PreviousDockerfile, PreviousManifestJSON, and Feedback seed a refine
	// run; ignored for inventory.
	PreviousDockerfile   string
	PreviousManifestJSON string
	Feedback             string
	// Argv overrides the harness's headless argv template; the task
	// placeholder is substituted with the rendered prompt. Tests and the
	// stub-driven gateway tests use it.
	Argv []string
	// Timeout bounds each harness invocation; zero means the default ten
	// minutes.
	Timeout time.Duration
}

// ScanResult is a validated inventory: the Dockerfile, the raw manifest
// text as the agent wrote it, and the parsed items.
type ScanResult struct {
	Dockerfile   string
	ManifestJSON string
	Manifest     []domain.ManifestItem
}

// scanRetryNote closes the retry prompt an inventory, repo, or refine run
// gets after failed validation.
const scanRetryNote = "Correct these problems and write both files again."

// RunScan runs one environment inventory on this machine: it renders the
// versioned prompt, runs the chosen harness headless in a scratch
// directory under a hard timeout, validates the Dockerfile and
// manifest.json the agent wrote, and retries once with the validation
// error appended to the prompt before giving up. The scratch directory is
// removed after every attempt, success or failure. progress may be nil;
// when set it is called serially, so callers need no locking. Failures
// are returned as *ScanFailure carrying the last output lines.
func RunScan(ctx context.Context, opts ScanOptions, progress func(ScanEvent)) (*ScanResult, error) {
	emit := serialEmitter(progress)
	repoPath, err := scanRepoPath(opts)
	if err != nil {
		return nil, err
	}
	if opts.Harness == "fake" {
		if opts.Mode == ScanModeRepo {
			return fakeScan(fakeRepoDockerfile, fakeRepoManifestJSON, "fake harness: returning the canned repo pair", emit)
		}
		return fakeScan(fakeScanDockerfile, fakeScanManifestJSON, "fake harness: returning the canned inventory", emit)
	}
	argv, err := scanArgvTemplate(opts.Harness, opts.Argv)
	if err != nil {
		return nil, err
	}
	return runScanLoop(ctx, argv, repoPath, opts.Timeout, scanRetryNote, emit,
		func(scratch string) (string, error) { return renderScanPrompt(opts, scratch) },
		collectScanOutput)
}

// scanRepoPath returns the repository a run is anchored in: required and
// validated for repo mode, honored on refine runs whose pair came from a
// repo scan, empty otherwise.
func scanRepoPath(opts ScanOptions) (string, error) {
	switch {
	case opts.Mode == ScanModeRepo,
		opts.Mode == ScanModeRefine && opts.RepoPath != "":
		if err := validateRepoPath(opts.RepoPath); err != nil {
			return "", err
		}
		return opts.RepoPath, nil
	default:
		return "", nil
	}
}

// renderScanPrompt renders the versioned prompt for the scan's mode.
// Repo-anchored runs name scratch as the output directory, since their
// working directory is the repository the agent must never write into.
func renderScanPrompt(opts ScanOptions, scratch string) (string, error) {
	switch opts.Mode {
	case ScanModeInventory:
		return envprompt.RenderInventory(envprompt.InventoryParams{BaseImage: envdef.BaseImage})
	case ScanModeRepo:
		return envprompt.RenderRepo(envprompt.RepoParams{BaseImage: envdef.BaseImage, OutputDir: scratch})
	case ScanModeRefine:
		params := envprompt.RefineParams{
			Dockerfile:   opts.PreviousDockerfile,
			ManifestJSON: opts.PreviousManifestJSON,
			Feedback:     opts.Feedback,
		}
		if opts.RepoPath != "" {
			params.OutputDir = scratch
		}
		return envprompt.RenderRefine(params)
	default:
		return "", fmt.Errorf("localops: unknown scan mode %q (want %s, %s, or %s)", opts.Mode, ScanModeInventory, ScanModeRepo, ScanModeRefine)
	}
}

// collectScanOutput reads and validates the two files the output contract
// requires the agent to write into the scratch directory.
func collectScanOutput(scratch string) (*ScanResult, error) {
	dockerfile, err := readContractFile(scratch, "Dockerfile")
	if err != nil {
		return nil, err
	}
	manifestJSON, err := readContractFile(scratch, "manifest.json")
	if err != nil {
		return nil, err
	}
	items, err := envdef.ParseManifest(manifestJSON)
	if err != nil {
		return nil, err
	}
	if err := envdef.ValidateDockerfile(string(dockerfile), items); err != nil {
		return nil, err
	}
	return &ScanResult{
		Dockerfile:   string(dockerfile),
		ManifestJSON: string(manifestJSON),
		Manifest:     items,
	}, nil
}

func readContractFile(scratch, name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(scratch, name))
	if err != nil {
		return nil, fmt.Errorf("the agent did not write %s into its working directory: %w", name, err)
	}
	return data, nil
}

// Canned pair for the "fake" harness: a real, buildable inventory with
// one apt item, so demos and tests exercise the whole mirror flow without
// a vendor CLI. It must always satisfy the envdef contract.
const fakeScanDockerfile = `FROM ubuntu:24.04

RUN apt-get update \
    && apt-get install -y --no-install-recommends jq=1.7.1-3build1 \
    && rm -rf /var/lib/apt/lists/*
`

const fakeScanManifestJSON = `[
  {
    "name": "jq",
    "version": "1.7.1",
    "reason": "canned inventory item for demos and tests",
    "start_line": 3,
    "end_line": 5,
    "check_command": "jq --version"
  }
]
`

// Canned pair for a fake repo scan, distinct from the mirror pair so the
// from-repo flow is demoable end to end. It must always satisfy the
// envdef contract.
const fakeRepoDockerfile = `FROM ubuntu:24.04

RUN apt-get update \
    && apt-get install -y --no-install-recommends ripgrep=14.1.0-1 \
    && rm -rf /var/lib/apt/lists/*
`

const fakeRepoManifestJSON = `[
  {
    "name": "ripgrep",
    "version": "14.1.0",
    "reason": "canned repo item for demos and tests",
    "start_line": 3,
    "end_line": 5,
    "check_command": "rg --version"
  }
]
`

// fakeScan returns a canned pair through the same validation path a real
// scan uses, so the fakes can never drift from the contract.
func fakeScan(dockerfile, manifestJSON, line string, emit func(ScanEvent)) (*ScanResult, error) {
	emit(ScanEvent{Status: ScanStatusRunning})
	emit(ScanEvent{Line: line})
	emit(ScanEvent{Status: ScanStatusValidating})
	items, err := envdef.ParseManifest([]byte(manifestJSON))
	if err != nil {
		return nil, fmt.Errorf("localops: canned fake manifest: %w", err)
	}
	if err := envdef.ValidateDockerfile(dockerfile, items); err != nil {
		return nil, fmt.Errorf("localops: canned fake Dockerfile: %w", err)
	}
	return &ScanResult{
		Dockerfile:   dockerfile,
		ManifestJSON: manifestJSON,
		Manifest:     items,
	}, nil
}
