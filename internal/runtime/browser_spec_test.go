package runtime

import (
	"math"
	"strings"
	"testing"
)

func TestBrowserSpecRejectsUnboundedResourcesAndUnpinnedImages(t *testing.T) {
	valid := BrowserSpec{RunContainer: "run-container", Image: "aether/browser:1.0.0", CreationKey: "creation", ControlHostPath: "/private/browser/control", CPULimit: 1, MemoryLimitBytes: 1 << 30}
	for _, test := range []struct {
		name   string
		change func(*BrowserSpec)
	}{
		{"zero-cpu", func(s *BrowserSpec) { s.CPULimit = 0 }},
		{"nan-cpu", func(s *BrowserSpec) { s.CPULimit = math.NaN() }},
		{"infinite-cpu", func(s *BrowserSpec) { s.CPULimit = math.Inf(1) }},
		{"zero-memory", func(s *BrowserSpec) { s.MemoryLimitBytes = 0 }},
		{"unversioned-image", func(s *BrowserSpec) { s.Image = "aether/browser" }},
		{"latest-image", func(s *BrowserSpec) { s.Image = "aether/browser:latest" }},
		{"host-root-mount", func(s *BrowserSpec) { s.ControlHostPath = "/" }},
		{"relative-mount", func(s *BrowserSpec) { s.ControlHostPath = "control" }},
		{"missing-owner", func(s *BrowserSpec) { s.RunContainer = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := valid
			test.change(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("accepted an unsafe browser specification")
			}
		})
	}
	for _, image := range []string{valid.Image, "sha256:" + strings.Repeat("a", 64), "registry.example/browser@sha256:" + strings.Repeat("b", 64)} {
		spec := valid
		spec.Image = image
		if err := spec.Validate(); err != nil {
			t.Fatalf("rejected explicit browser image %q: %v", image, err)
		}
	}
}
