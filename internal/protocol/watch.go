package protocol

// Watch methods. A watch belongs to the member who created it: only they
// list, read, change or delete it, admins included.
const (
	MethodWatchCreate     = "watch.create"
	MethodWatchList       = "watch.list"
	MethodWatchGet        = "watch.get"
	MethodWatchUpdate     = "watch.update"
	MethodWatchDelete     = "watch.delete"
	MethodNotifyPrefsGet  = "notify.prefs.get"
	MethodNotifyPrefsSet  = "notify.prefs.set"
	WatchNotificationsMax = 50
)

// Watch target kinds.
const (
	WatchTargetRun     = "run"
	WatchTargetMission = "mission"
	WatchTargetPR      = "pr"
)

// Watch conditions. A target kind accepts only its own subset.
const (
	WatchNeedsYou  = "needs_you"
	WatchFinished  = "finished"
	WatchPRMerged  = "pr_merged"
	WatchCI        = "ci"
	WatchDelivered = "delivered"
	WatchReview    = "review"
	WatchMerged    = "merged"
	WatchClosed    = "closed"
)

// Watch states.
const (
	WatchActive  = "active"
	WatchPaused  = "paused"
	WatchError   = "error"
	WatchDone    = "done"
	WatchExpired = "expired"
)

// Watch sources.
const (
	WatchSourceDashboard = "dashboard"
	WatchSourceAssistant = "assistant"
	WatchSourceMCP       = "mcp"
)

// Notification delivery channels and statuses.
const (
	WatchChannelPush    = "push"
	WatchChannelDesktop = "desktop"
	WatchChannelInApp   = "in_app"

	// WatchStatusPending is a push still to be sent: being sent, waiting
	// for the member to leave a dashboard, or waiting for a retry.
	WatchStatusPending       = "pending"
	WatchStatusDelivered     = "delivered"
	WatchStatusNoDestination = "no_destination"
	WatchStatusDisabled      = "disabled"
	WatchStatusHeldQuiet     = "held_quiet_hours"
	WatchStatusFailed        = "failed"
	// WatchStatusCleared is a held push dropped because the need it
	// announced was resolved before it could be sent.
	WatchStatusCleared = "cleared"
)

type WatchTarget struct {
	Kind      string `json:"kind"`
	RunID     string `json:"run_id,omitempty"`
	MissionID string `json:"mission_id,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Number    int    `json:"number,omitempty"`
}

type WatchChecks struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
}

// WatchObserved is what the last evaluation read about the target.
type WatchObserved struct {
	Summary     string       `json:"summary"`
	RunStatus   string       `json:"run_status,omitempty"`
	Activity    string       `json:"activity,omitempty"`
	Need        string       `json:"need,omitempty"`
	Phase       string       `json:"phase,omitempty"`
	PRState     string       `json:"pr_state,omitempty"`
	HeadSHA     string       `json:"head_sha,omitempty"`
	Checks      *WatchChecks `json:"checks,omitempty"`
	ReviewState string       `json:"review_state,omitempty"`
}

type Watch struct {
	ID               string             `json:"id"`
	MemberID         string             `json:"member_id"`
	WorkspaceID      string             `json:"workspace_id"`
	Target           WatchTarget        `json:"target"`
	TargetTitle      string             `json:"target_title"`
	Conditions       []string           `json:"conditions"`
	State            string             `json:"state"`
	Error            string             `json:"error,omitempty"`
	Label            string             `json:"label,omitempty"`
	Source           string             `json:"source"`
	CreatedAt        string             `json:"created_at"`
	UpdatedAt        string             `json:"updated_at"`
	ExpiresAt        string             `json:"expires_at"`
	LastCheckAt      string             `json:"last_check_at,omitempty"`
	NextCheckAt      string             `json:"next_check_at,omitempty"`
	Observed         WatchObserved      `json:"observed"`
	LastNotification *WatchNotification `json:"last_notification,omitempty"`
	Link             string             `json:"link"`
}

type WatchDelivery struct {
	Channel string `json:"channel"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	At      string `json:"at"`
}

type WatchNotification struct {
	ID         string          `json:"id"`
	WatchID    string          `json:"watch_id"`
	Condition  string          `json:"condition"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	Link       string          `json:"link"`
	CreatedAt  string          `json:"created_at"`
	Deliveries []WatchDelivery `json:"deliveries"`
}

type WatchCreateParams struct {
	Target         WatchTarget `json:"target"`
	Conditions     []string    `json:"conditions,omitempty"`
	ExpiresAt      string      `json:"expires_at,omitempty"`
	Label          string      `json:"label,omitempty"`
	Source         string      `json:"source,omitempty"`
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
}

// WatchResult carries the watch and one human line saying what it watches
// and where its notices go.
type WatchResult struct {
	Watch   Watch  `json:"watch"`
	Receipt string `json:"receipt"`
}

type WatchListParams struct {
	State      string `json:"state,omitempty"`
	TargetKind string `json:"target_kind,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	MissionID  string `json:"mission_id,omitempty"`
}

type WatchListResult struct {
	Watches []Watch `json:"watches"`
}

type WatchIDParams struct {
	WatchID string `json:"watch_id"`
}

// WatchGetResult lists the watch's notifications newest first, at most
// WatchNotificationsMax.
type WatchGetResult struct {
	Watch         Watch               `json:"watch"`
	Notifications []WatchNotification `json:"notifications"`
}

type WatchUpdateParams struct {
	WatchID    string   `json:"watch_id"`
	Paused     *bool    `json:"paused,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	Label      *string  `json:"label,omitempty"`
}

type QuietHours struct {
	Start string `json:"start"`
	End   string `json:"end"`
	TZ    string `json:"tz"`
}

// NotifyPrefs is how a member's watch notices reach them. Push sends them
// to their Web Push devices; during QuietHours push is held and one push
// is sent when they end.
type NotifyPrefs struct {
	Push       bool        `json:"push"`
	QuietHours *QuietHours `json:"quiet_hours,omitempty"`
	// Destinations is read-only: notify.prefs.set ignores it.
	Destinations NotifyDestinations `json:"destinations"`
}

// NotifyDestinations counts where a member's notices can go.
type NotifyDestinations struct {
	PushDevices int `json:"push_devices"`
}
