package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// EnvironmentSource records which path produced an environment definition.
type EnvironmentSource string

const (
	// EnvironmentSourceMirror is an inventory of the creator's machine.
	EnvironmentSourceMirror EnvironmentSource = "mirror"
	// EnvironmentSourceRepo is an inventory of the repository contents.
	EnvironmentSourceRepo EnvironmentSource = "repo"
	// EnvironmentSourceStandard is the prebuilt standard environment.
	EnvironmentSourceStandard EnvironmentSource = "standard"
	// EnvironmentSourceManual is a hand-written definition.
	EnvironmentSourceManual EnvironmentSource = "manual"
)

// Valid reports whether s is a defined environment source.
func (s EnvironmentSource) Valid() bool {
	switch s {
	case EnvironmentSourceMirror, EnvironmentSourceRepo, EnvironmentSourceStandard, EnvironmentSourceManual:
		return true
	}
	return false
}

// EnvironmentStatus is the lifecycle state of an environment definition
// version.
//
// The lifecycle is:
//
//	saved -> building -> verifying -> active | failed
//
// Exactly one version per workspace is active at a time.
type EnvironmentStatus string

const (
	EnvironmentSaved     EnvironmentStatus = "saved"
	EnvironmentBuilding  EnvironmentStatus = "building"
	EnvironmentVerifying EnvironmentStatus = "verifying"
	EnvironmentActive    EnvironmentStatus = "active"
	EnvironmentFailed    EnvironmentStatus = "failed"
)

// Valid reports whether s is a defined environment status.
func (s EnvironmentStatus) Valid() bool {
	switch s {
	case EnvironmentSaved, EnvironmentBuilding, EnvironmentVerifying, EnvironmentActive, EnvironmentFailed:
		return true
	}
	return false
}

// ManifestItem is one human-readable claim about the environment: what is
// installed, at which version, why, which Dockerfile lines install it, and
// the command whose output must contain the version during post-build
// verification.
type ManifestItem struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Reason  string `json:"reason,omitempty"`
	// StartLine and EndLine are the 1-based inclusive Dockerfile line span
	// the item maps to; removing the item removes those lines.
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	CheckCommand string `json:"check_command"`
}

// Validate checks the fields every manifest item must carry.
func (m ManifestItem) Validate() error {
	switch {
	case strings.TrimSpace(m.Name) == "":
		return errors.New("manifest item name is required")
	case strings.TrimSpace(m.Version) == "":
		return fmt.Errorf("manifest item %q: version is required", m.Name)
	case strings.TrimSpace(m.CheckCommand) == "":
		return fmt.Errorf("manifest item %q: check command is required", m.Name)
	case m.StartLine < 1 || m.EndLine < m.StartLine:
		return fmt.Errorf("manifest item %q: line span %d-%d is not a valid 1-based range", m.Name, m.StartLine, m.EndLine)
	}
	return nil
}

// EnvironmentDefinition is one versioned workspace environment: the
// Dockerfile the server builds, the manifest it is verified against, and
// the provenance and lifecycle state of that version. The store assigns
// Version at save time; zero means not yet saved.
type EnvironmentDefinition struct {
	WorkspaceID WorkspaceID       `json:"workspace_id"`
	Version     int               `json:"version"`
	Dockerfile  string            `json:"dockerfile"`
	Manifest    []ManifestItem    `json:"manifest"`
	Source      EnvironmentSource `json:"source"`
	// Harness is the agent harness that wrote the definition; empty for
	// sources with no agent behind them (standard, manual).
	Harness string            `json:"harness,omitempty"`
	Status  EnvironmentStatus `json:"status"`
	// FailureDetail explains a failed status, e.g. per-item verification
	// mismatches; empty otherwise.
	FailureDetail string    `json:"failure_detail,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Validate checks the structural rules every definition must satisfy.
// Dockerfile content rules (base image, forbidden instructions, line spans
// against the file) are the envdef validator's job.
func (d EnvironmentDefinition) Validate() error {
	switch {
	case d.WorkspaceID == "":
		return errors.New("environment definition workspace ID is required")
	case d.Version < 0:
		return fmt.Errorf("environment definition version %d must not be negative", d.Version)
	case strings.TrimSpace(d.Dockerfile) == "":
		return errors.New("environment definition Dockerfile is empty")
	case len(d.Manifest) == 0:
		return errors.New("environment definition manifest is empty")
	case !d.Source.Valid():
		return fmt.Errorf("unknown environment source %q", d.Source)
	case !d.Status.Valid():
		return fmt.Errorf("unknown environment status %q", d.Status)
	}
	for _, item := range d.Manifest {
		if err := item.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ImageTag returns the local-only docker tag for this definition version:
// aether/ws-<workspace-id>:<version>. The workspace ID, not the name, keys
// the tag because names can change.
func (d EnvironmentDefinition) ImageTag() string {
	return fmt.Sprintf("aether/ws-%s:%d", d.WorkspaceID, d.Version)
}
