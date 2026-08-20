package server

import (
	"strings"
	"testing"

	"github.com/distribution/reference"
)

// TestDefaultNeutralImageIsValidDockerReference pins the deployment bug where
// an uppercase repository owner ("3xDevOps") made every default-image pull
// fail with "repository name must be lowercase".
func TestDefaultNeutralImageIsValidDockerReference(t *testing.T) {
	if _, err := reference.ParseNormalizedNamed(DefaultNeutralImage); err != nil {
		t.Fatalf("DefaultNeutralImage %q is not a valid Docker reference: %v", DefaultNeutralImage, err)
	}
	repo, _, ok := strings.Cut(DefaultNeutralImage, ":")
	if !ok {
		t.Fatalf("DefaultNeutralImage %q has no explicit tag", DefaultNeutralImage)
	}
	if repo != strings.ToLower(repo) {
		t.Fatalf("DefaultNeutralImage repository %q must be lowercase", repo)
	}
}

func TestNeutralImageTag(t *testing.T) {
	cases := []struct {
		version, want string
	}{
		{"v0.1.2-alpha.1", "v0.1.2-alpha.1"},
		{"v0.1.2", "v0.1.2"},
		{"v0.1.2-alpha.1-3-g35e2990", "v0.1.2-alpha.1"},
		{"v0.1.2-alpha.1-3-g35e2990-dirty", "v0.1.2-alpha.1"},
		{"v0.1.2-dirty", "v0.1.2"},
		{"dev", "latest"},
		{"35e2990", "latest"},
		{"", "latest"},
	}
	for _, tc := range cases {
		if got := neutralImageTag(tc.version); got != tc.want {
			t.Errorf("neutralImageTag(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}
