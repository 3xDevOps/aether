package server

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/distribution/reference"
)

// TestDefaultStandardImageIsValidDockerReference guards the standard image
// default the same way: lowercase repository, explicit tag.
func TestDefaultStandardImageIsValidDockerReference(t *testing.T) {
	if _, err := reference.ParseNormalizedNamed(DefaultStandardImage); err != nil {
		t.Fatalf("DefaultStandardImage %q is not a valid Docker reference: %v", DefaultStandardImage, err)
	}
	repo, _, ok := strings.Cut(DefaultStandardImage, ":")
	if !ok {
		t.Fatalf("DefaultStandardImage %q has no explicit tag", DefaultStandardImage)
	}
	if repo != strings.ToLower(repo) {
		t.Fatalf("DefaultStandardImage repository %q must be lowercase", repo)
	}
}

func TestReleaseImageTag(t *testing.T) {
	cases := []struct {
		version, want string
	}{
		{"v0.1.2-alpha.1", "v0.1.2-alpha.1"},
		{"v0.1.2", "v0.1.2"},
		{"v0.1.2-alpha.1-3-g35e2990", "v0.1.2-alpha.1"},
		{"v0.1.2-alpha.1-3-g35e2990-dirty", "v0.1.2-alpha.1"},
		{"v0.1.2-dirty", "v0.1.2"},
		{"v1.2.3-rc-1", "v1.2.3-rc-1"},
		{"v1.2.3-rc-1-5-gabcdef0", "v1.2.3-rc-1"},
		{"dev", "latest"},
		{"35e2990", "latest"},
		{"", "latest"},
	}
	for _, tc := range cases {
		if got := releaseImageTag(tc.version); got != tc.want {
			t.Errorf("releaseImageTag(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}

type runTitleSetterSpy struct {
	run   domain.RunID
	title string
	calls int
}

func (s *runTitleSetterSpy) SetRunTitle(run domain.RunID, title string) {
	s.run = run
	s.title = title
	s.calls++
}

func TestForwardRunTitleIgnoresNonRunSession(t *testing.T) {
	spy := &runTitleSetterSpy{}
	forwardRunTitle(spy, ptyhost.TerminalSession("member-1", "main"), "terminal title")
	if spy.calls != 0 {
		t.Fatalf("terminal title forwarded %d times, want 0", spy.calls)
	}

	forwardRunTitle(spy, ptyhost.RunSession("run-1"), "run title")
	if spy.calls != 1 || spy.run != "run-1" || spy.title != "run title" {
		t.Fatalf("run title forwarding = %#v, want one call for run-1", spy)
	}
}
