package envprompt

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// templateHashes pins the embedded template's content to Version. Editing
// prompt.tmpl without bumping Version fails here; bumping Version without
// recording the new hash fails here too.
var templateHashes = map[int]string{
	1: "443382544fa970566cf85f1312f29fa40ffe75e4f71ab599e8fd4cd444ae98be",
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
