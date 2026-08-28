// Package envdef validates the environment-definition output contract:
// the Dockerfile plus manifest pair that agents, the dashboard, and the
// CLI hand to the server before anything is stored or built. It is shared
// with the local gateway, so it must not import server-only packages.
package envdef

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/secretscan"
)

// BaseImage is the only base image an environment Dockerfile may build on.
const BaseImage = "ubuntu:24.04"

// ParseManifest decodes manifest JSON into validated manifest items. Every
// invalid item is reported, numbered by its 1-based position so nameless
// items are still identifiable.
func ParseManifest(data []byte) ([]domain.ManifestItem, error) {
	var items []domain.ManifestItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("envdef: manifest is not a JSON list of items: %w", err)
	}
	if len(items) == 0 {
		return nil, errors.New("envdef: manifest has no items")
	}
	var errs []error
	for i, item := range items {
		if err := item.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("item %d: %w", i+1, err))
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("envdef: invalid manifest: %w", errors.Join(errs...))
	}
	return items, nil
}

// ValidateDockerfile checks the Dockerfile text against the output
// contract and the manifest's line spans, returning every violation
// joined: a single build stage based on BaseImage, no COPY or ADD (the
// build context is the Dockerfile alone), no credential-shaped content,
// and each manifest item mapping to lines that exist in the file.
func ValidateDockerfile(dockerfile string, manifest []domain.ManifestItem) error {
	lines := strings.Split(strings.TrimSuffix(dockerfile, "\n"), "\n")
	var errs []error

	fromCount := 0
	continued := false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if continued {
			continued = strings.HasSuffix(line, "\\")
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		continued = strings.HasSuffix(line, "\\")
		fields := strings.Fields(line)
		instruction := strings.ToUpper(fields[0])
		if instruction == "ONBUILD" && len(fields) > 1 {
			instruction = strings.ToUpper(fields[1])
		}
		switch instruction {
		case "FROM":
			fromCount++
			if fromCount > 1 {
				continue
			}
			if len(fields) < 2 || fields[1] != BaseImage {
				errs = append(errs, fmt.Errorf("line %d: the build stage must be based on %s", i+1, BaseImage))
			}
		case "COPY", "ADD":
			errs = append(errs, fmt.Errorf("line %d: %s is forbidden; the build context is the Dockerfile alone", i+1, instruction))
		}
	}
	if fromCount == 0 {
		errs = append(errs, fmt.Errorf("no FROM instruction; the Dockerfile must start a build stage from %s", BaseImage))
	}
	if fromCount > 1 {
		errs = append(errs, fmt.Errorf("%d build stages; a single stage is required", fromCount))
	}

	for _, finding := range secretscan.Scan("Dockerfile", []byte(dockerfile)) {
		errs = append(errs, fmt.Errorf("credential-shaped content at %s (%s); images must carry no secrets", finding.Location, finding.Kind))
	}

	for _, item := range manifest {
		if item.EndLine > len(lines) {
			errs = append(errs, fmt.Errorf("manifest item %q: line span %d-%d ends past the Dockerfile's %d lines", item.Name, item.StartLine, item.EndLine, len(lines)))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("envdef: invalid Dockerfile: %w", errors.Join(errs...))
	}
	return nil
}
