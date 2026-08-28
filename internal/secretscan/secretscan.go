// Package secretscan wraps the gitleaks default detector so both server
// packages and client-shared validators can reject credential-shaped
// content with one deny vocabulary. It must stay free of server-only
// imports.
package secretscan

import (
	"fmt"
	"sync"

	"github.com/zricethezav/gitleaks/v8/detect"
)

// Finding is one secret-scanner hit in a scanned file.
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

// Scan runs the established gitleaks detector over one file.
func Scan(rel string, content []byte) []Finding {
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
