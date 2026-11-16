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
	// MethodServerReleaseChannelSet changes the release channel the
	// server and its clients follow, or with dry_run reports what the
	// change would do (super admin only).
	MethodServerReleaseChannelSet = "server.release_channel_set"
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
//
// NotDeclined, on a cancel, says that the cancelled release's build could
// not be recorded as declined, so the server may schedule it again by
// itself.
type ServerUpdateResult struct {
	Status      string `json:"status"`
	Version     string `json:"version,omitempty"`
	RequestedBy string `json:"requested_by,omitempty"`
	RequestedAt string `json:"requested_at,omitempty"`
	NotDeclined string `json:"not_declined,omitempty"`
}

// ServerUpdateStatusParams are the params of server.update_status.
// Refresh runs the server's release check before answering instead of
// reporting the last background check.
type ServerUpdateStatusParams struct {
	Refresh bool `json:"refresh,omitempty"`
}

// PendingServerUpdate is one recorded update waiting for an idle server.
//
// ByServer marks an update the server scheduled itself, when its release
// check found a newer build on its channel; RequestedBy is then "server".
type PendingServerUpdate struct {
	Version     string `json:"version"`
	RequestedBy string `json:"requested_by"`
	RequestedAt string `json:"requested_at"`
	ByServer    bool   `json:"by_server,omitempty"`
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
//
// Channel is the release channel the server follows; ChannelPending is
// set while a new server has not chosen one and follows beta. The latest
// fields describe the newest release on Channel, and are empty until a
// check on it has run. ChannelWait reports that the running build is above
// it: the server stays on its build until the channel reaches it. Builds
// are the signed (build, hotfix) of each release, 0 for a release from
// before channels. Declined is the build the server no longer schedules by
// itself: a super admin cancelled it or rolled back from it, or it failed;
// the server schedules only a build above it. ChannelPointers is each
// channel's verified pointer at the last check: its version, build and
// published_at, so a frozen channel shows its age. DeclineNote repeats
// the last cancel's not_declined, and ScheduleError is why the server last
// failed to schedule the newest release by itself, such as its preflight.
type ServerUpdateStatusResult struct {
	ServerVersion   string                    `json:"server_version"`
	ServerBuild     int                       `json:"server_build,omitempty"`
	ServerHotfix    int                       `json:"server_hotfix,omitempty"`
	Channel         string                    `json:"channel,omitempty"`
	ChannelPending  bool                      `json:"channel_pending,omitempty"`
	ChannelWait     bool                      `json:"channel_wait,omitempty"`
	Declined        *ReleaseBuild             `json:"declined,omitempty"`
	DeclineNote     string                    `json:"decline_note,omitempty"`
	ScheduleError   string                    `json:"schedule_error,omitempty"`
	ChannelPointers map[string]ChannelPointer `json:"channel_pointers,omitempty"`
	LatestVersion   string                    `json:"latest_version,omitempty"`
	LatestChannel   string                    `json:"latest_channel,omitempty"`
	LatestBuild     int                       `json:"latest_build,omitempty"`
	LatestHotfix    int                       `json:"latest_hotfix,omitempty"`
	NotesURL        string                    `json:"notes_url,omitempty"`
	CheckedAt       string                    `json:"checked_at,omitempty"`
	CheckError      string                    `json:"check_error,omitempty"`
	Phase           string                    `json:"phase"`
	Error           string                    `json:"error,omitempty"`
	FromVersion     string                    `json:"from_version,omitempty"`
	ToVersion       string                    `json:"to_version,omitempty"`
	Latest          string                    `json:"latest,omitempty"`
	UpdateAvailable bool                      `json:"update_available"`
	Capable         bool                      `json:"capable"`
	Incapable       string                    `json:"incapable,omitempty"`
	Pending         *PendingServerUpdate      `json:"pending,omitempty"`
	Waiting         *ServerUpdateWaiting      `json:"waiting,omitempty"`
	Last            *ServerUpdateAttempt      `json:"last,omitempty"`
	ManualCommands  []string                  `json:"manual_commands,omitempty"`
}

// ChannelPointer is what a channel's signed pointer named at the server's
// last release check, for display: present in channel_pointers only for a
// pointer the server fetched and verified.
type ChannelPointer struct {
	Version     string `json:"version"`
	Build       int    `json:"build,omitempty"`
	Hotfix      int    `json:"hotfix,omitempty"`
	PublishedAt string `json:"published_at"`
}

// ServerReleaseChannelSetParams are the params of
// server.release_channel_set (super admin only). Channel is "stable",
// "beta" or "nightly". DryRun reports the result without saving.
type ServerReleaseChannelSetParams struct {
	Channel string `json:"channel"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

// ReleaseBuild is a release and its place in the release order: the signed
// channel, build and hotfix, build 0 for a release from before channels.
type ReleaseBuild struct {
	Version string `json:"version"`
	Channel string `json:"channel,omitempty"`
	Build   int    `json:"build"`
	Hotfix  int    `json:"hotfix,omitempty"`
}

// ServerReleaseChannelSetResult is the result of
// server.release_channel_set: what a confirmation says before the change.
// Target is the newest release on Channel by its signed pointer, nil when
// it could not be read, and TargetError then says why. Wait reports that
// Running is above Target, so the server stays on its build until Channel
// reaches it. Saved is false for a dry run. Retired is the pending update
// the change retires because the new channel does not take its release,
// recorded as failed with the reason. Kept is a pending update the change
// left in place because its manifest could not be fetched to check it,
// and KeptError is the fetch error; the server still refuses it at its
// idle moment if the channel does not take it.
type ServerReleaseChannelSetResult struct {
	Channel     string        `json:"channel"`
	Previous    string        `json:"previous"`
	Saved       bool          `json:"saved"`
	Running     ReleaseBuild  `json:"running"`
	Target      *ReleaseBuild `json:"target,omitempty"`
	TargetError string        `json:"target_error,omitempty"`
	Wait        bool          `json:"wait"`
	Retired     string        `json:"retired,omitempty"`
	Kept        string        `json:"kept,omitempty"`
	KeptError   string        `json:"kept_error,omitempty"`
}
