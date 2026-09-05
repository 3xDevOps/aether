package envprompt

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

// templateHashes pins the embedded template's content to Version. Editing
// prompt.tmpl without bumping Version fails here; bumping Version without
// recording the new hash fails here too.
var templateHashes = map[int]string{
	5: "18c50452df7785edcaf9d37bc9c4080e879f8030a7edd2e1792744ae4c5856bb",
}

func TestVersionPinsTemplate(t *testing.T) {
	want, ok := templateHashes[Version]
	if !ok {
		t.Fatalf("no recorded hash for Version %d: add it to templateHashes", Version)
	}
	sum := sha256.Sum256([]byte(templateText))
	got := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("prompt.tmpl changed but Version is still %d: bump Version and record hash %s", Version, got)
	}
}

// profileClauses are distinctive phrases, one per profile-prompt clause,
// in the order the template must state them: what is being decided, the
// embedded inventory, the never-read limits, the judgement rule, the
// output contract, and the rules that contract enforces.
var profileClauses = []string{
	"which of this machine's coding-agent configurations",
	"<<<INVENTORY",
	"INVENTORY",
	"Never open a profile file",
	"never read credential stores",
	"carries no file contents",
	"leaves out every credential file",
	"the user's own working setup",
	"nothing but defaults",
	"Write exactly one file, profile.json, into",
	"\"harnesses\"",
	"\"import\"",
	"\"categories\"",
	"\"reason\"",
	"One entry per harness the inventory names",
	"only category names from that harness's own inventory",
	"non-empty when \"import\" is true",
	"one sentence on one line, under 300 characters",
	"No other keys, and no other files",
}

// profileTestInventory stands in for one rendered harness inventory.
const profileTestInventory = `harness: claude
root: /home/dev/.claude
files: 3, bytes: 120
category skills: 2 files, 80 bytes
  skills/review/SKILL.md
  skills/ship/SKILL.md`

func TestRenderProfile(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "aether-profilescan-1234")
	prompt, err := RenderProfile(ProfileParams{
		Inventory: profileTestInventory,
		OutputDir: outputDir,
	})
	if err != nil {
		t.Fatalf("RenderProfile: %v", err)
	}
	if !strings.Contains(prompt, profileTestInventory) {
		t.Errorf("profile prompt does not embed the inventory verbatim:\n%s", prompt)
	}
	if !strings.Contains(prompt, outputDir) {
		t.Errorf("profile prompt does not name the output directory %q", outputDir)
	}
	if !strings.Contains(prompt, "No repository was given") {
		t.Errorf("profile prompt without a repository does not say so:\n%s", prompt)
	}
	flat := strings.Join(strings.Fields(prompt), " ")
	pos := 0
	for _, clause := range profileClauses {
		at := strings.Index(flat[pos:], clause)
		if at < 0 {
			t.Errorf("profile prompt is missing the clause %q after position %d", clause, pos)
			continue
		}
		pos += at
	}
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "}}") {
		t.Errorf("prompt has unrendered template markers:\n%s", prompt)
	}
}

// TestRenderProfileRepoAnchored: a run given a repository names it, says
// the repository may not be changed, and still sends profile.json to the
// absolute scratch directory - never to the current directory, which is
// the repository itself.
func TestRenderProfileRepoAnchored(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "aether-profilescan-5678")
	repo := filepath.Join(t.TempDir(), "project")
	prompt, err := RenderProfile(ProfileParams{
		Inventory: profileTestInventory,
		OutputDir: outputDir,
		RepoPath:  repo,
	})
	if err != nil {
		t.Fatalf("RenderProfile: %v", err)
	}
	if !strings.Contains(prompt, repo) {
		t.Errorf("repo-anchored profile prompt does not name the repository %q", repo)
	}
	if !strings.Contains(prompt, outputDir) {
		t.Errorf("repo-anchored profile prompt does not name the output directory %q", outputDir)
	}
	if strings.Contains(prompt, "the current directory") {
		t.Errorf("repo-anchored profile prompt points at the current directory:\n%s", prompt)
	}
	if strings.Contains(prompt, "No repository was given") {
		t.Errorf("repo-anchored profile prompt still claims no repository:\n%s", prompt)
	}
	flat := strings.Join(strings.Fields(prompt), " ")
	if !strings.Contains(flat, "Never modify, create, or delete repository files") {
		t.Errorf("repo-anchored profile prompt does not forbid touching repository files:\n%s", prompt)
	}
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "}}") {
		t.Errorf("prompt has unrendered template markers:\n%s", prompt)
	}
}

func TestRenderProfileRequiresParams(t *testing.T) {
	absOut := filepath.Join(t.TempDir(), "out")
	for name, params := range map[string]ProfileParams{
		"inventory":                 {OutputDir: absOut},
		"output directory":          {Inventory: profileTestInventory},
		"absolute output directory": {Inventory: profileTestInventory, OutputDir: "relative/out"},
	} {
		if _, err := RenderProfile(params); err == nil {
			t.Errorf("RenderProfile accepted a missing %s", name)
		}
	}
}
