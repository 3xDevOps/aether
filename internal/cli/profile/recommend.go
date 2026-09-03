package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// maxReasonRunes bounds the one sentence a recommendation carries per
// harness. It is a checkable claim shown next to a checkbox, not a
// report.
const maxReasonRunes = 300

// HarnessRecommendation is the agent's verdict for one harness profile.
type HarnessRecommendation struct {
	Harness string `json:"harness"`
	Import  bool   `json:"import"`
	// Categories are the preview categories worth importing. Non-empty
	// when Import is true, empty otherwise.
	Categories []string `json:"categories"`
	// Reason is one sentence a developer can check against the file list.
	Reason string `json:"reason"`
}

// Recommendation is the whole JSON document a profile scan produces: one
// entry per harness the agent was shown.
type Recommendation struct {
	Harnesses []HarnessRecommendation `json:"harnesses"`
}

// ParseRecommendation validates the JSON an agent wrote against the
// previews it was given. It is deliberately strict, the way a manifest
// is: the output drives which of the user's own directories get
// uploaded, so an entry naming a harness or a category that was never in
// the inventory is a failed contract, not a value to sanitize. Every
// violation is reported at once so the one retry can fix them together.
func ParseRecommendation(data []byte, previews []Preview) (Recommendation, error) {
	var rec Recommendation
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return Recommendation{}, fmt.Errorf("profile: recommendation is not the expected JSON object: %w", err)
	}
	if len(rec.Harnesses) == 0 {
		return Recommendation{}, errors.New("profile: recommendation has no harnesses")
	}
	known := map[string][]string{}
	for _, p := range previews {
		known[p.Harness] = p.CategoryNames()
	}
	var errs []error
	seen := map[string]bool{}
	for i, h := range rec.Harnesses {
		where := fmt.Sprintf("harness %d", i+1)
		if h.Harness != "" {
			where = fmt.Sprintf("harness %q", h.Harness)
		}
		categories, ok := known[h.Harness]
		switch {
		case h.Harness == "":
			errs = append(errs, fmt.Errorf("%s: name is empty", where))
		case !ok:
			errs = append(errs, fmt.Errorf("%s: was not in the inventory", where))
		case seen[h.Harness]:
			errs = append(errs, fmt.Errorf("%s: appears twice", where))
		}
		seen[h.Harness] = true
		switch {
		case strings.TrimSpace(h.Reason) == "":
			errs = append(errs, fmt.Errorf("%s: reason is empty", where))
		case len([]rune(h.Reason)) > maxReasonRunes:
			errs = append(errs, fmt.Errorf("%s: reason is longer than %d characters", where, maxReasonRunes))
		case strings.Contains(h.Reason, "\n"):
			errs = append(errs, fmt.Errorf("%s: reason must be one sentence on one line", where))
		}
		if !h.Import {
			if len(h.Categories) > 0 {
				errs = append(errs, fmt.Errorf("%s: import is false but categories are listed", where))
			}
			continue
		}
		if len(h.Categories) == 0 {
			errs = append(errs, fmt.Errorf("%s: import is true but no categories are listed", where))
		}
		for _, c := range h.Categories {
			if ok && !slices.Contains(categories, c) {
				errs = append(errs, fmt.Errorf("%s: category %q is not one this machine has (%s)",
					where, c, strings.Join(categories, ", ")))
			}
		}
	}
	if len(errs) > 0 {
		return Recommendation{}, fmt.Errorf("profile: invalid recommendation: %w", errors.Join(errs...))
	}
	return rec, nil
}
