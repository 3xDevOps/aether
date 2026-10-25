package protocol

// The Slack methods manage the caller's link to the server's Slack bot
// (docs/slack.md).
const (
	// MethodSlackStatus reports whether the server has a Slack bot and
	// whether the caller has linked a Slack account to it.
	MethodSlackStatus = "slack.status"
	// MethodSlackLink mints a one-use code the caller sends the bot in a
	// direct message to link their Slack account, bound to the caller's
	// edge device.
	MethodSlackLink = "slack.link"
	// MethodSlackUnlink removes the caller's link.
	MethodSlackUnlink = "slack.unlink"
)

// SlackStatusResult is the server's bot and the caller's link. Configured
// is false when an admin has not set Slack up; the other fields are then
// empty. Bot is the bot's name and Team the Slack workspace it is in,
// empty until the server first reaches Slack. Error is why the server is
// not connected to Slack now.
type SlackStatusResult struct {
	Configured bool       `json:"configured"`
	Connected  bool       `json:"connected"`
	Bot        string     `json:"bot,omitempty"`
	Team       string     `json:"team,omitempty"`
	Error      string     `json:"error,omitempty"`
	Link       *SlackLink `json:"link,omitempty"`
}

// SlackLink is the Slack account a member linked, in RFC 3339 for
// LinkedAt.
type SlackLink struct {
	UserID   string `json:"user_id"`
	TeamID   string `json:"team_id"`
	LinkedAt string `json:"linked_at"`
}

// SlackLinkResult is the code to send the bot and when it expires, in
// RFC 3339.
type SlackLinkResult struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	Bot       string `json:"bot,omitempty"`
	Team      string `json:"team,omitempty"`
}

// SlackUnlinkResult is the link as it was before its removal.
type SlackUnlinkResult struct {
	Link SlackLink `json:"link"`
}
