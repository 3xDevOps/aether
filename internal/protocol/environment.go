package protocol

const (
	// MethodWorkspaceEnvironmentGet reads a workspace's setup script and
	// variables. A secret is listed by name; its value is never returned.
	MethodWorkspaceEnvironmentGet = "workspace.environment.get"
	// MethodWorkspaceEnvironmentSet changes them (admin only).
	MethodWorkspaceEnvironmentSet = "workspace.environment.set"
)

const (
	MaxWorkspaceSetupScriptBytes   = 64 << 10
	MaxWorkspaceVariableValueBytes = 64 << 10
)

// WorkspaceVariable is one environment variable of a workspace. Value is
// empty for a secret on read.
type WorkspaceVariable struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type WorkspaceEnvironmentGetParams struct {
	WorkspaceID string `json:"workspace_id"`
}

// WorkspaceEnvironmentSetParams: Set adds or replaces variables, Unset
// removes them, and a non-nil SetupScript replaces the script ("" clears
// it). Everything not named is kept.
type WorkspaceEnvironmentSetParams struct {
	WorkspaceID string              `json:"workspace_id"`
	SetupScript *string             `json:"setup_script,omitempty"`
	Set         []WorkspaceVariable `json:"set,omitempty"`
	Unset       []string            `json:"unset,omitempty"`
}

// WorkspaceEnvironmentResult lists Variables sorted by name.
type WorkspaceEnvironmentResult struct {
	WorkspaceID string              `json:"workspace_id"`
	SetupScript string              `json:"setup_script"`
	Variables   []WorkspaceVariable `json:"variables"`
}

// SetupFailure is the Data of a launch refused by the workspace setup
// script: what the script printed, with secret values masked.
type SetupFailure struct {
	SetupOutput string `json:"setup_output"`
}
