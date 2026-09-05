package localops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/cli/profile"
	"github.com/3xDevOps/Aether/internal/envprompt"
)

// profileRecommendationFile is the single file a profile scan's output
// contract asks the agent to write into the scratch directory.
const profileRecommendationFile = "profile.json"

// profileRetryNote closes the retry prompt a profile run gets after
// failed validation.
const profileRetryNote = "Correct these problems and write profile.json again."

// ProfileScanOptions parameterizes one RunProfileScan call.
type ProfileScanOptions struct {
	// Harness names the setup-capable harness to run, or "fake" for a
	// canned recommendation that exercises the flow without a vendor CLI.
	Harness string
	// RepoPath is optional. When set the run is repo-anchored exactly as a
	// repo scan is: the repository is the working directory, the agent may
	// read it but never write it, and the scan fails if its git status
	// changes. Empty means the agent reasons from the inventories alone.
	RepoPath string
	// Inventory is what the agent is shown: one preview per harness whose
	// configuration directory is present on this machine. Required and
	// non-empty; the recommendation is validated against it.
	Inventory []profile.Preview
	// Argv overrides the harness's headless argv template; the task
	// placeholder is substituted with the rendered prompt. Tests and the
	// stub-driven gateway tests use it.
	Argv []string
	// Timeout bounds each harness invocation; zero means the default ten
	// minutes.
	Timeout time.Duration
}

// ProfileScanResult is a validated recommendation: the raw JSON text as
// the agent wrote it, and the parsed document.
type ProfileScanResult struct {
	RecommendationJSON string
	Recommendation     profile.Recommendation
}

// RunProfileScan asks a coding agent which of this machine's agent
// configurations are worth importing into Aether. It renders the
// versioned profile prompt around the given inventories, runs the chosen
// harness headless under a hard timeout, validates the profile.json the
// agent wrote against those same inventories, and retries once with the
// validation error appended before giving up. The agent is shown names,
// paths, counts and sizes only: never file contents, and never the
// excluded credential files. progress may be nil; when set it is called
// serially. Failures are returned as *ScanFailure carrying the last
// output lines.
func RunProfileScan(ctx context.Context, opts ProfileScanOptions, progress func(ScanEvent)) (*ProfileScanResult, error) {
	emit := serialEmitter(progress)
	if len(opts.Inventory) == 0 {
		return nil, errors.New("localops: a profile scan needs at least one harness inventory")
	}
	if opts.RepoPath != "" {
		if err := validateProfileRepoPath(opts.RepoPath); err != nil {
			return nil, err
		}
	}
	if opts.Harness == "fake" {
		return fakeProfileScan(opts.Inventory, emit)
	}
	argv, err := scanArgvTemplate(opts.Harness, opts.Argv)
	if err != nil {
		return nil, err
	}
	inventory := formatProfileInventory(opts.Inventory)
	return runScanLoop(ctx, argv, opts.RepoPath, opts.Timeout, profileRetryNote, emit,
		func(scratch string) (string, error) {
			return envprompt.RenderProfile(envprompt.ProfileParams{
				Inventory: inventory,
				OutputDir: scratch,
				RepoPath:  opts.RepoPath,
			})
		},
		func(scratch string) (*ProfileScanResult, error) {
			return collectProfileOutput(scratch, opts.Inventory)
		})
}

func validateProfileRepoPath(repoPath string) error {
	if strings.TrimSpace(repoPath) == "" {
		return errors.New("localops: a profile scan needs the repository's folder")
	}
	info, err := os.Stat(repoPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("localops: the folder %s does not exist", repoPath)
	case err != nil:
		return fmt.Errorf("localops: check the folder %s: %w", repoPath, err)
	case !info.IsDir():
		return fmt.Errorf("localops: %s is not a folder", repoPath)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return fmt.Errorf("localops: %s is not a git repository (it has no .git entry)", repoPath)
	}
	return nil
}

// formatProfileInventory renders the previews as the plain text block the
// prompt embeds. It carries names, paths, counts and sizes only; file
// contents and the Excluded list are deliberately absent, since an
// excluded entry names a credential file or a file a secret scanner
// flagged.
func formatProfileInventory(previews []profile.Preview) string {
	var b strings.Builder
	for i, p := range previews {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "harness: %s\nroot: %s\nfiles: %d, bytes: %d\n", p.Harness, p.Root, p.Files, p.Bytes)
		if len(p.Categories) == 0 {
			b.WriteString("  (no files)\n")
			continue
		}
		for _, c := range p.Categories {
			fmt.Fprintf(&b, "  category %s: %d files, %d bytes\n", c.Name, c.Files, c.Bytes)
			for _, path := range c.Paths {
				fmt.Fprintf(&b, "    %s\n", path)
			}
			if c.Truncated {
				fmt.Fprintf(&b, "    (path list cut here; the %d-file count above is exact)\n", c.Files)
			}
		}
	}
	return b.String()
}

// collectProfileOutput reads and validates the one file the profile
// output contract requires, against the inventories the agent was shown.
func collectProfileOutput(scratch string, previews []profile.Preview) (*ProfileScanResult, error) {
	data, err := os.ReadFile(filepath.Join(scratch, profileRecommendationFile))
	if err != nil {
		return nil, fmt.Errorf("the agent did not write %s into its output directory: %w", profileRecommendationFile, err)
	}
	rec, err := profile.ParseRecommendation(data, previews)
	if err != nil {
		return nil, err
	}
	return &ProfileScanResult{RecommendationJSON: string(data), Recommendation: rec}, nil
}

// fakeProfileScan answers the "fake" harness with a recommendation built
// from the inventory it was given and run through the same validation a
// real scan uses, so the fake can never drift from the contract. It
// imports every harness that has categories to import.
func fakeProfileScan(previews []profile.Preview, emit func(ScanEvent)) (*ProfileScanResult, error) {
	emit(ScanEvent{Status: ScanStatusRunning})
	emit(ScanEvent{Line: "fake harness: recommending every configuration that has files"})
	emit(ScanEvent{Status: ScanStatusValidating})

	rec := profile.Recommendation{Harnesses: make([]profile.HarnessRecommendation, 0, len(previews))}
	for _, p := range previews {
		categories := p.CategoryNames()
		entry := profile.HarnessRecommendation{Harness: p.Harness}
		if len(categories) == 0 {
			entry.Reason = fmt.Sprintf("canned answer: %s has no files to import.", p.Harness)
			rec.Harnesses = append(rec.Harnesses, entry)
			continue
		}
		entry.Import = true
		entry.Categories = categories
		entry.Reason = fmt.Sprintf("canned answer: %s has %d files under %s.", p.Harness, p.Files, strings.Join(categories, ", "))
		rec.Harnesses = append(rec.Harnesses, entry)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("localops: canned fake recommendation: %w", err)
	}
	parsed, err := profile.ParseRecommendation(data, previews)
	if err != nil {
		return nil, fmt.Errorf("localops: canned fake recommendation: %w", err)
	}
	return &ProfileScanResult{RecommendationJSON: string(data), Recommendation: parsed}, nil
}
