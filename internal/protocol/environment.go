package protocol

import (
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Workspace environment methods, all admin-guarded: a Dockerfile build is
// arbitrary code on the server's docker daemon, so every call that stores
// or builds a definition is workspace administration.
const (
	// MethodEnvSave validates and stores a new environment definition
	// version for a workspace.
	MethodEnvSave = "env.save"
	// MethodEnvBuild starts building a stored definition version into the
	// workspace image; progress rides the environment.build event stream.
	MethodEnvBuild = "env.build"
	// MethodEnvStatus reads a workspace's definition versions and statuses.
	MethodEnvStatus = "env.status"
	// MethodEnvRollback re-activates the previous environment version.
	MethodEnvRollback = "env.rollback"
	// MethodEnvEdit asks an agent harness to revise the workspace's
	// environment from a plain-language request. The call returns as soon
	// as the edit run is launched; progress, the proposed version, and any
	// failure arrive as environment.edit events.
	MethodEnvEdit = "env.edit"
	// MethodEnvGet reads one stored definition version in full - the
	// Dockerfile the status surface deliberately omits - optionally with a
	// unified diff of the Dockerfile against another version.
	MethodEnvGet = "env.get"
)

// EnvSaveParams are the params of env.save. Manifest is the manifest JSON
// exactly as the producing flow wrote it; the server validates it together
// with the Dockerfile before storing anything.
type EnvSaveParams struct {
	Workspace  WorkspaceSelector `json:"workspace"`
	Dockerfile string            `json:"dockerfile"`
	Manifest   json.RawMessage   `json:"manifest"`
	// Source is the producing path: mirror, repo, standard, or manual.
	Source string `json:"source"`
	// Harness is the agent harness that wrote the definition; empty for
	// sources with no agent behind them.
	Harness string `json:"harness,omitempty"`
}

// EnvSaveResult is the result of env.save: the assigned version number.
type EnvSaveResult struct {
	Version int `json:"version"`
}

// EnvBuildParams are the params of env.build. Version zero builds the
// active version, or the newest saved one when nothing is active yet.
type EnvBuildParams struct {
	Workspace WorkspaceSelector `json:"workspace"`
	Version   int               `json:"version,omitempty"`
}

// EnvBuildResult is the result of env.build: the version now building.
// The call returns as soon as the build is launched; completion and
// failure arrive on the event stream.
type EnvBuildResult struct {
	Version int `json:"version"`
}

// EnvStatusParams are the params of env.status.
type EnvStatusParams struct {
	Workspace WorkspaceSelector `json:"workspace"`
}

// EnvironmentVersion is one definition version in an env.status result.
// The manifest doubles as the human-readable environment summary.
type EnvironmentVersion struct {
	Version       int                      `json:"version"`
	Source        domain.EnvironmentSource `json:"source"`
	Harness       string                   `json:"harness,omitempty"`
	Status        domain.EnvironmentStatus `json:"status"`
	FailureDetail string                   `json:"failure_detail,omitempty"`
	Active        bool                     `json:"active,omitempty"`
	Manifest      []domain.ManifestItem    `json:"manifest"`
	CreatedAt     string                   `json:"created_at"`
	UpdatedAt     string                   `json:"updated_at"`
}

// EnvironmentVersionFromDomain converts one stored definition version to
// its wire form; the Dockerfile itself stays server-side.
func EnvironmentVersionFromDomain(d *domain.EnvironmentDefinition) EnvironmentVersion {
	return EnvironmentVersion{
		Version:       d.Version,
		Source:        d.Source,
		Harness:       d.Harness,
		Status:        d.Status,
		FailureDetail: d.FailureDetail,
		Active:        d.Status == domain.EnvironmentActive,
		Manifest:      d.Manifest,
		CreatedAt:     rfc3339(d.CreatedAt),
		UpdatedAt:     rfc3339(d.UpdatedAt),
	}
}

// EnvStatusResult is the result of env.status: every version newest
// first. ActiveVersion is zero when no version is active.
type EnvStatusResult struct {
	Versions      []EnvironmentVersion `json:"versions"`
	ActiveVersion int                  `json:"active_version,omitempty"`
}

// EnvRollbackParams are the params of env.rollback. The server picks the
// rollback target itself - the most recent previously good version - so
// the params carry only the workspace.
type EnvRollbackParams struct {
	Workspace WorkspaceSelector `json:"workspace"`
}

// EnvRollbackResult is the result of env.rollback: the version that is
// active again.
type EnvRollbackResult struct {
	Version int `json:"version"`
}

// EnvEditParams are the params of env.edit: which agent harness runs the
// edit and the admin's change request in plain language.
type EnvEditParams struct {
	Workspace WorkspaceSelector `json:"workspace"`
	Harness   string            `json:"harness"`
	Request   string            `json:"request"`
}

// EnvEditResult is the result of env.edit: the edit run is launched and
// everything after that - agent output, the proposed version, failure -
// rides the environment.edit event stream.
type EnvEditResult struct {
	Accepted bool `json:"accepted"`
}

// EnvGetParams are the params of env.get. Version names the stored
// definition to fetch; DiffAgainst optionally names another version to
// diff this version's Dockerfile against.
type EnvGetParams struct {
	Workspace   WorkspaceSelector `json:"workspace"`
	Version     int               `json:"version"`
	DiffAgainst int               `json:"diff_against,omitempty"`
}

// EnvGetResult is the result of env.get: one version's full content.
// Diff is a git unified diff of the Dockerfile from the DiffAgainst
// version to this one, present only when requested and the files differ.
type EnvGetResult struct {
	Version    int                      `json:"version"`
	Dockerfile string                   `json:"dockerfile"`
	Manifest   []domain.ManifestItem    `json:"manifest"`
	Source     domain.EnvironmentSource `json:"source"`
	Harness    string                   `json:"harness,omitempty"`
	Status     domain.EnvironmentStatus `json:"status"`
	Diff       string                   `json:"diff,omitempty"`
}
