package protocol

// Server self-update methods. server.update is admin only; the status
// method is readable by any member so a client can say why the update
// button is missing without being allowed to press it.
const (
	// MethodServerUpdate asks the server to replace its own binaries and
	// restart, now or at the next idle moment (admin only).
	MethodServerUpdate = "server.update"
	// MethodServerUpdateStatus reports the server's own update state.
	MethodServerUpdateStatus = "server.update_status"
)

// When values of ServerUpdateParams.
const (
	// ServerUpdateNow applies the update immediately and restarts,
	// dropping attached terminals and live syncs.
	ServerUpdateNow = "now"
	// ServerUpdateIdle records one pending update, applied the first time
	// no run is active and no workspace shell is open.
	ServerUpdateIdle = "idle"
	// ServerUpdateCancel clears the pending update.
	ServerUpdateCancel = "cancel"
)

// Status values of ServerUpdateResult.
const (
	ServerUpdateApplying  = "applying"
	ServerUpdateScheduled = "scheduled"
	ServerUpdateCancelled = "cancelled"
)

// Outcome values of ServerUpdateAttempt.
const (
	// ServerUpdateApplied: the binaries were replaced and the server
	// re-executed on the new version.
	ServerUpdateApplied = "applied"
	// ServerUpdateFailed: nothing was replaced (or the swap could not be
	// restarted into); Detail carries the real error.
	ServerUpdateFailed = "failed"
)

// ServerUpdateParams are the params of server.update (admin only).
// Version is a release tag ("v" plus semver); empty means the latest
// published release. When is one of the ServerUpdate* when constants.
type ServerUpdateParams struct {
	Version string `json:"version,omitempty"`
	When    string `json:"when"`
}

// ServerUpdateResult is the result of server.update. Status is
// "applying", "scheduled", or "cancelled"; the remaining fields echo the
// update the call recorded and are empty for a cancel.
type ServerUpdateResult struct {
	Status      string `json:"status"`
	Version     string `json:"version,omitempty"`
	RequestedBy string `json:"requested_by,omitempty"`
	RequestedAt string `json:"requested_at,omitempty"`
}

// PendingServerUpdate is one recorded update waiting for an idle server.
type PendingServerUpdate struct {
	Version     string `json:"version"`
	RequestedBy string `json:"requested_by"`
	RequestedAt string `json:"requested_at"`
}

// ServerUpdateAttempt is the outcome of the last update the server tried.
type ServerUpdateAttempt struct {
	Version string `json:"version"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
	At      string `json:"at"`
}

// ServerUpdateStatusResult is the result of server.update_status, readable
// by any member.
//
// Capable is false when the server process cannot replace its own binary -
// the documented unprivileged install, where the binary directory is not
// writable by the service user. ManualCommands then carries the commands
// to run on the server host instead, and server.update refuses with
// CodeInvalidState.
type ServerUpdateStatusResult struct {
	ServerVersion   string               `json:"server_version"`
	Latest          string               `json:"latest,omitempty"`
	UpdateAvailable bool                 `json:"update_available"`
	Capable         bool                 `json:"capable"`
	Pending         *PendingServerUpdate `json:"pending,omitempty"`
	Last            *ServerUpdateAttempt `json:"last,omitempty"`
	ManualCommands  []string             `json:"manual_commands,omitempty"`
}
