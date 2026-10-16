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
	// no run is working and no workspace shell is open.
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
	// ServerUpdateFailed: nothing was replaced; Detail carries the real
	// error.
	ServerUpdateFailed = "failed"
	// ServerUpdateRolledBack: the new binary did not boot, so the previous
	// binaries (and database, when a migration ran) were restored; Detail
	// carries the real error.
	ServerUpdateRolledBack = "rolled_back"
)

// Phase values of ServerUpdateStatusResult. Applied, failed, and
// rolled_back describe the last attempt and stay until the next one.
const (
	ServerUpdatePhaseIdle       = "idle"
	ServerUpdatePhaseScheduled  = "scheduled"
	ServerUpdatePhasePreflight  = "preflight"
	ServerUpdatePhaseApplying   = "applying"
	ServerUpdatePhaseRestarting = "restarting"
	ServerUpdatePhaseVerifying  = "verifying"
	ServerUpdatePhaseApplied    = "applied"
	ServerUpdatePhaseFailed     = "failed"
	ServerUpdatePhaseRolledBack = "rolled_back"
)

// ServerUpdateParams are the params of server.update (admin only).
// Version is a release tag ("v" plus semver); empty means the latest
// published release. When is one of the ServerUpdate* when constants.
// AllowDowngrade permits a Version older than the running release; it is
// refused with When "idle", because a downgrade is applied by hand.
type ServerUpdateParams struct {
	Version        string `json:"version,omitempty"`
	When           string `json:"when"`
	AllowDowngrade bool   `json:"allow_downgrade,omitempty"`
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

// ServerUpdateStatusParams are the params of server.update_status.
// Refresh runs the server's release check before answering instead of
// reporting the last background check.
type ServerUpdateStatusParams struct {
	Refresh bool `json:"refresh,omitempty"`
}

// PendingServerUpdate is one recorded update waiting for an idle server.
type PendingServerUpdate struct {
	Version     string `json:"version"`
	RequestedBy string `json:"requested_by"`
	RequestedAt string `json:"requested_at"`
}

// ServerUpdateWaiting says what a pending update has not applied yet for.
// Paused runs are listed but do not hold anything back: a frozen container
// survives a restart like a live one, and nothing is working inside it.
type ServerUpdateWaiting struct {
	Runs   int `json:"runs"`
	Paused int `json:"paused"`
	Shells int `json:"shells"`
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
// most often the documented unprivileged install, where the binary
// directory is not writable by the service user. Incapable then says which
// reason it is, ManualCommands carries the commands to run on the server
// host instead, and server.update refuses with CodeInvalidState.
//
// Waiting is set when a pending update has not applied yet because the
// server is busy; it names what it is waiting for.
//
// LatestVersion, NotesURL, CheckedAt, and CheckError come from the
// server's own background release check; Latest repeats LatestVersion for
// older clients. Phase is where the current or last update is; Error,
// FromVersion, and ToVersion describe that update.
type ServerUpdateStatusResult struct {
	ServerVersion   string               `json:"server_version"`
	LatestVersion   string               `json:"latest_version,omitempty"`
	NotesURL        string               `json:"notes_url,omitempty"`
	CheckedAt       string               `json:"checked_at,omitempty"`
	CheckError      string               `json:"check_error,omitempty"`
	Phase           string               `json:"phase"`
	Error           string               `json:"error,omitempty"`
	FromVersion     string               `json:"from_version,omitempty"`
	ToVersion       string               `json:"to_version,omitempty"`
	Latest          string               `json:"latest,omitempty"`
	UpdateAvailable bool                 `json:"update_available"`
	Capable         bool                 `json:"capable"`
	Incapable       string               `json:"incapable,omitempty"`
	Pending         *PendingServerUpdate `json:"pending,omitempty"`
	Waiting         *ServerUpdateWaiting `json:"waiting,omitempty"`
	Last            *ServerUpdateAttempt `json:"last,omitempty"`
	ManualCommands  []string             `json:"manual_commands,omitempty"`
}
