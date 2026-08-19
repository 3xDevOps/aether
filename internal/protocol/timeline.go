package protocol

// Wave 4 session timeline method.
const (
	// MethodSessionTimeline pages one session's persisted history.
	MethodSessionTimeline = "session.timeline"
)

// SessionTimelineParams selects one page of a session's history. RunID,
// MemberID (the event's actor), and Types narrow it; AfterSeq is the
// cursor from the previous page's NextSeq, zero for the beginning. Limit
// zero means the server's default page size.
type SessionTimelineParams struct {
	SessionID string   `json:"session_id"`
	RunID     string   `json:"run_id,omitempty"`
	MemberID  string   `json:"member_id,omitempty"`
	Types     []string `json:"types,omitempty"`
	AfterSeq  uint64   `json:"after_seq,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

// SessionTimelineResult is one page of history, oldest first, in the same
// Event envelope the event feed streams. More reports that history
// remains after NextSeq - a page can be empty and still have more, since
// filtered-out events advance the cursor too.
type SessionTimelineResult struct {
	Events  []Event `json:"events"`
	NextSeq uint64  `json:"next_seq"`
	More    bool    `json:"more"`
}
