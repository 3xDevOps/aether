package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeWatchChanged tells a watch's owner to read it again. NotificationID
// is set when the change recorded a notice. It is published under
// ServerScope and streamed to the owner alone.
const TypeWatchChanged Type = "watch.changed"

type WatchChangedPayload struct {
	WatchID        string          `json:"watch_id"`
	MemberID       domain.MemberID `json:"member_id"`
	NotificationID string          `json:"notification_id,omitempty"`
}

func (WatchChangedPayload) EventType() Type { return TypeWatchChanged }

// Recipient is the one member a watch change may be streamed to.
func (p WatchChangedPayload) Recipient() domain.MemberID { return p.MemberID }

func init() { registerPayload[WatchChangedPayload](TypeWatchChanged) }
