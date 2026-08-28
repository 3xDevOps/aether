package envdef

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

const wellFormedDockerfile = `FROM ubuntu:24.04
RUN apt-get update && \
    apt-get install -y golang-go=2:1.22~2build1
RUN apt-get install -y jq
`

const wellFormedManifest = `[
  {
    "name": "go",
    "version": "1.22",
    "reason": "repository language",
    "start_line": 2,
    "end_line": 3,
    "check_command": "go version"
  },
  {
    "name": "jq",
    "version": "1.7",
    "start_line": 4,
    "end_line": 4,
    "check_command": "jq --version"
  }
]`

func TestWellFormedPairPasses(t *testing.T) {
	items, err := ParseManifest([]byte(wellFormedManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(items) != 2 || items[0].Name != "go" || items[1].Name != "jq" {
		t.Fatalf("ParseManifest returned unexpected items: %+v", items)
	}
	if err := ValidateDockerfile(wellFormedDockerfile, items); err != nil {
		t.Fatalf("ValidateDockerfile: %v", err)
	}
}

func TestParseManifestRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"name": "go"`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestParseManifestRejectsEmptyManifest(t *testing.T) {
	if _, err := ParseManifest([]byte(`[]`)); err == nil {
		t.Fatal("empty manifest accepted")
	}
}

func TestParseManifestRejectsMissingFieldsNamingTheItem(t *testing.T) {
	tests := map[string]string{
		"missing check command": `[{"name": "go", "version": "1.22", "start_line": 2, "end_line": 3}]`,
		"missing version":       `[{"name": "go", "start_line": 2, "end_line": 3, "check_command": "go version"}]`,
		"missing line span":     `[{"name": "go", "version": "1.22", "check_command": "go version"}]`,
	}
	for name, manifest := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(manifest))
			if err == nil {
				t.Fatal("invalid manifest item accepted")
			}
			if !strings.Contains(err.Error(), `"go"`) {
				t.Fatalf("error does not name the offending item: %v", err)
			}
		})
	}
}

func TestParseManifestNamesItemWithoutName(t *testing.T) {
	_, err := ParseManifest([]byte(`[
	  {"name": "go", "version": "1.22", "start_line": 2, "end_line": 3, "check_command": "go version"},
	  {"version": "1.7", "start_line": 4, "end_line": 4, "check_command": "jq --version"}
	]`))
	if err == nil {
		t.Fatal("item without a name accepted")
	}
	if !strings.Contains(err.Error(), "item 2") {
		t.Fatalf("error does not locate the nameless item: %v", err)
	}
}

func TestValidateDockerfileRejections(t *testing.T) {
	manifest := []domain.ManifestItem{{
		Name:         "go",
		Version:      "1.22",
		StartLine:    2,
		EndLine:      2,
		CheckCommand: "go version",
	}}
	tests := map[string]struct {
		dockerfile string
		want       string
	}{
		"wrong base image": {
			dockerfile: "FROM debian:12\nRUN apt-get install -y golang-go\n",
			want:       "ubuntu:24.04",
		},
		"missing FROM": {
			dockerfile: "RUN apt-get update\nRUN apt-get install -y golang-go\n",
			want:       "FROM",
		},
		"multiple build stages": {
			dockerfile: "FROM ubuntu:24.04\nRUN apt-get install -y golang-go\nFROM ubuntu:24.04\n",
			want:       "single",
		},
		"COPY forbidden": {
			dockerfile: "FROM ubuntu:24.04\ncopy hostfile /etc/hostfile\n",
			want:       "COPY",
		},
		"ADD forbidden": {
			dockerfile: "FROM ubuntu:24.04\nADD https://example.com/tool.tar.gz /opt\n",
			want:       "ADD",
		},
		"ONBUILD smuggled COPY forbidden": {
			dockerfile: "FROM ubuntu:24.04\nONBUILD COPY hostfile /etc/hostfile\n",
			want:       "COPY",
		},
		"credential-shaped ENV line": {
			dockerfile: "FROM ubuntu:24.04\nENV GITHUB_TOKEN=ghp_x7GkQ92LmN4pRt8vWz3JhBcD5fYs6qAe1XuT\n",
			want:       "credential",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateDockerfile(tc.dockerfile, manifest)
			if err == nil {
				t.Fatal("invalid Dockerfile accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateDockerfileRejectsOutOfRangeSpanNamingTheItem(t *testing.T) {
	manifest := []domain.ManifestItem{{
		Name:         "go",
		Version:      "1.22",
		StartLine:    2,
		EndLine:      9,
		CheckCommand: "go version",
	}}
	err := ValidateDockerfile("FROM ubuntu:24.04\nRUN apt-get install -y golang-go\n", manifest)
	if err == nil {
		t.Fatal("out-of-range line span accepted")
	}
	if !strings.Contains(err.Error(), `"go"`) {
		t.Fatalf("error does not name the offending item: %v", err)
	}
}

func TestValidateDockerfileReportsEveryViolation(t *testing.T) {
	manifest := []domain.ManifestItem{{
		Name:         "go",
		Version:      "1.22",
		StartLine:    2,
		EndLine:      9,
		CheckCommand: "go version",
	}}
	err := ValidateDockerfile("FROM debian:12\nCOPY hostfile /etc/hostfile\n", manifest)
	if err == nil {
		t.Fatal("invalid Dockerfile accepted")
	}
	for _, want := range []string{"ubuntu:24.04", "COPY", `"go"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("joined error %q is missing violation %q", err, want)
		}
	}
}

func TestValidateDockerfileIgnoresInstructionWordsInArguments(t *testing.T) {
	dockerfile := "FROM ubuntu:24.04\nRUN echo COPY ADD FROM && \\\n    echo COPY continued\n"
	manifest := []domain.ManifestItem{{
		Name:         "echo",
		Version:      "1",
		StartLine:    2,
		EndLine:      3,
		CheckCommand: "echo 1",
	}}
	if err := ValidateDockerfile(dockerfile, manifest); err != nil {
		t.Fatalf("instruction words inside RUN arguments were rejected: %v", err)
	}
}
