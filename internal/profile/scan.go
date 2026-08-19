package profile

import (
	"fmt"
	"sync"

	"github.com/zricethezav/gitleaks/v8/detect"
)

// Finding is one secret-scanner hit in a profile file.
type Finding struct {
	Path     string
	Location string
	Kind     string
}

var (
	detectorOnce sync.Once
	detector     *detect.Detector
	detectorErr  error
)

func secretDetector() (*detect.Detector, error) {
	detectorOnce.Do(func() {
		detector, detectorErr = detect.NewDetectorDefaultConfig()
	})
	return detector, detectorErr
}

// ScanContent runs the established gitleaks detector over one file.
func ScanContent(rel string, content []byte) []Finding {
	d, err := secretDetector()
	if err != nil {
		return []Finding{{Path: rel, Location: "0:0", Kind: "scanner unavailable"}}
	}
	// detect.Fragment is deprecated in favour of sources.Fragment, but Detect
	// only accepts the former until gitleaks v9, and DetectString drops the
	// file path that the path-based rules need.
	hits := d.Detect(detect.Fragment{ //nolint:staticcheck // SA1019: no v8 alternative carries FilePath
		Raw:      string(content),
		Bytes:    content,
		FilePath: rel,
	})
	out := make([]Finding, 0, len(hits))
	for _, h := range hits {
		kind := h.RuleID
		if kind == "" {
			kind = h.Description
		}
		out = append(out, Finding{
			Path:     rel,
			Location: fmt.Sprintf("%d:%d", h.StartLine, h.StartColumn),
			Kind:     kind,
		})
	}
	return out
}

// ScanFiles rejects files that gitleaks flags unless allow[path] is set.
// Paths in allow are slash-separated and relative to the profile root.
func ScanFiles(files []File, allow map[string]bool) error {
	for _, f := range files {
		if allow[f.Path] {
			continue
		}
		hits := ScanContent(f.Path, f.Content)
		if len(hits) == 0 {
			continue
		}
		h := hits[0]
		return fmt.Errorf("%w: secret detected in %s at %s (%s)", ErrDenied, h.Path, h.Location, h.Kind)
	}
	return nil
}
