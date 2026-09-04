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
	1: "443382544fa970566cf85f1312f29fa40ffe75e4f71ab599e8fd4cd444ae98be",
	2: "039d12aac2cbecca59ae17e43b7a4453c913e754c01cc8237ff31a5ef13e18fa",
	3: "1fac34339b6c0a016a64f153b37d44b028fd3d907bdbbbd09fa2bd28fee82799",
	4: "3789bd17ba4fec8c2ea5abb671f0e34683d7b93c0cac452bd549fb71c018e29d",
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

// contractClauses are distinctive phrases, one per contract clause, in the
// order the template must state them.
var contractClauses = []string{
	"toolchains only",
	"exact versions",
	"Never record dotfiles, shell theming, credentials, or personal files",
	"never read credential stores",
	"Translate every finding",
	"Homebrew packages and darwin binaries",
	"exactly two files into the current directory",
	"single build stage FROM ubuntu:24.04",
	"no COPY or ADD",
	"no secrets",
	"stable-first",
	"manifest.json",
	"\"name\"",
	"\"version\"",
	"\"reason\"",
	"\"start_line\"",
	"\"end_line\"",
	"\"check_command\"",
	"output must contain the version",
	"only what this machine actually uses",
}

// assertClausesInOrder checks every contract clause is present and stated
// in the plan's order. Prompts wrap at the source line width, so matching
// runs on a whitespace-collapsed copy. Ordering is asserted from the
// contract's opening phrase on: the refine prompt legitimately repeats
// contract words earlier, inside the embedded previous pair.
func assertClausesInOrder(t *testing.T, prompt string) {
	t.Helper()
	flat := strings.Join(strings.Fields(prompt), " ")
	pos := strings.Index(flat, "Scope: toolchains only")
	if pos < 0 {
		t.Fatalf("prompt is missing the contract opening %q", "Scope: toolchains only")
	}
	for _, clause := range contractClauses {
		at := strings.Index(flat[pos:], clause)
		if at < 0 {
			t.Errorf("prompt is missing the clause %q after position %d", clause, pos)
			continue
		}
		pos += at
	}
}

func TestRenderInventory(t *testing.T) {
	prompt, err := RenderInventory(InventoryParams{BaseImage: "ubuntu:24.04"})
	if err != nil {
		t.Fatalf("RenderInventory: %v", err)
	}
	assertClausesInOrder(t, prompt)
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "}}") {
		t.Errorf("prompt has unrendered template markers:\n%s", prompt)
	}
}

func TestRenderInventoryRequiresBaseImage(t *testing.T) {
	if _, err := RenderInventory(InventoryParams{}); err == nil {
		t.Fatal("RenderInventory accepted an empty base image")
	}
}

func TestRenderRefine(t *testing.T) {
	params := RefineParams{
		Dockerfile:   "FROM ubuntu:24.04\nRUN apt-get install -y jq=1.7*\n",
		ManifestJSON: `[{"name":"jq","version":"1.7","start_line":2,"end_line":2,"check_command":"jq --version"}]`,
		Feedback:     "drop jq, I never use it",
	}
	prompt, err := RenderRefine(params)
	if err != nil {
		t.Fatalf("RenderRefine: %v", err)
	}
	for _, verbatim := range []string{params.Dockerfile, params.ManifestJSON, params.Feedback} {
		if !strings.Contains(prompt, verbatim) {
			t.Errorf("refine prompt does not embed %q verbatim", verbatim)
		}
	}
	assertClausesInOrder(t, prompt)
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "}}") {
		t.Errorf("prompt has unrendered template markers:\n%s", prompt)
	}
}

// repoClauses are distinctive phrases, one per repo-prompt clause, in
// the order the template must state them: derive from the repository's
// files, devcontainer as strongest intent, never touch repo files,
// write into the named output directory, then the shared file contract.
var repoClauses = []string{
	"from the repository's own files",
	"manifests, lockfiles, toolchain version files, and CI configs",
	"not the machine running this scan",
	".devcontainer/devcontainer.json",
	"strongest statement of intent",
	"Never modify, create, or delete repository files",
	"exactly two files, Dockerfile and manifest.json, into",
	"single build stage FROM ubuntu:24.04",
	"pinned",
	"no COPY or ADD",
	"no secrets",
	"stable-first",
	"manifest.json",
	"\"name\"",
	"\"version\"",
	"\"reason\"",
	"\"start_line\"",
	"\"end_line\"",
	"\"check_command\"",
	"output must contain the version",
	"only what the project actually needs",
}

func TestRenderRepo(t *testing.T) {
	// filepath.Join over t.TempDir() keeps the path absolute on every
	// platform; a bare /tmp literal is not absolute on Windows.
	outputDir := filepath.Join(t.TempDir(), "aether-env-scan-1234")
	prompt, err := RenderRepo(RepoParams{BaseImage: "ubuntu:24.04", OutputDir: outputDir})
	if err != nil {
		t.Fatalf("RenderRepo: %v", err)
	}
	if !strings.Contains(prompt, outputDir) {
		t.Errorf("repo prompt does not name the output directory %q verbatim", outputDir)
	}
	flat := strings.Join(strings.Fields(prompt), " ")
	pos := 0
	for _, clause := range repoClauses {
		at := strings.Index(flat[pos:], clause)
		if at < 0 {
			t.Errorf("repo prompt is missing the clause %q after position %d", clause, pos)
			continue
		}
		pos += at
	}
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "}}") {
		t.Errorf("prompt has unrendered template markers:\n%s", prompt)
	}
}

func TestRenderRepoRequiresParams(t *testing.T) {
	absOut := filepath.Join(t.TempDir(), "out")
	for name, params := range map[string]RepoParams{
		"base image":                {OutputDir: absOut},
		"output directory":          {BaseImage: "ubuntu:24.04"},
		"absolute output directory": {BaseImage: "ubuntu:24.04", OutputDir: "relative/out"},
	} {
		if _, err := RenderRepo(params); err == nil {
			t.Errorf("RenderRepo accepted a missing %s", name)
		}
	}
}

// TestRenderRefineWithOutputDir covers refine runs anchored in a
// repository: the prompt must send both files to the named scratch
// directory and forbid touching repository files, and must never point
// the agent at the current directory (the repository).
func TestRenderRefineWithOutputDir(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "aether-envscan-refine-1234")
	prompt, err := RenderRefine(RefineParams{
		Dockerfile:   "FROM ubuntu:24.04\nRUN apt-get install -y jq=1.7*\n",
		ManifestJSON: `[{"name":"jq","version":"1.7","start_line":2,"end_line":2,"check_command":"jq --version"}]`,
		Feedback:     "drop jq, I never use it",
		OutputDir:    outputDir,
	})
	if err != nil {
		t.Fatalf("RenderRefine: %v", err)
	}
	if !strings.Contains(prompt, outputDir) {
		t.Errorf("repo-anchored refine prompt does not name the output directory %q", outputDir)
	}
	if strings.Contains(prompt, "the current directory") {
		t.Errorf("repo-anchored refine prompt still points at the current directory:\n%s", prompt)
	}
	flat := strings.Join(strings.Fields(prompt), " ")
	if !strings.Contains(flat, "never modify, create, or delete") {
		t.Errorf("repo-anchored refine prompt does not forbid touching repository files:\n%s", prompt)
	}
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "}}") {
		t.Errorf("prompt has unrendered template markers:\n%s", prompt)
	}
}

func TestRenderRefineOutputDirMustBeAbsolute(t *testing.T) {
	if _, err := RenderRefine(RefineParams{
		Dockerfile:   "FROM ubuntu:24.04",
		ManifestJSON: "[]",
		Feedback:     "note",
		OutputDir:    "relative/out",
	}); err == nil {
		t.Fatal("RenderRefine accepted a relative output directory")
	}
}

func TestRenderRefineRequiresAllParams(t *testing.T) {
	full := RefineParams{Dockerfile: "FROM ubuntu:24.04", ManifestJSON: "[]", Feedback: "note"}
	for name, params := range map[string]RefineParams{
		"dockerfile": {ManifestJSON: full.ManifestJSON, Feedback: full.Feedback},
		"manifest":   {Dockerfile: full.Dockerfile, Feedback: full.Feedback},
		"feedback":   {Dockerfile: full.Dockerfile, ManifestJSON: full.ManifestJSON},
	} {
		if _, err := RenderRefine(params); err == nil {
			t.Errorf("RenderRefine accepted a missing %s", name)
		}
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
