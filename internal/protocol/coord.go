package protocol

// Coordination wire v1.
//
// This is the whole surface an agent reaches on its run's coordination
// socket: three methods, no control verbs, no git, no other run's
// transcript. It is versioned separately from the control channel because
// the bridge lives inside a container that outlives a server restart, and
// carries its version in every coord.status result so a bridge can tell
// what it is talking to.
const CoordWireVersion = "v1"

// The coordination method set. Nothing else is reachable on the socket.
const (
	MethodCoordStatus = "coord.status"
	MethodCoordSend   = "coord.send"
	MethodCoordInbox  = "coord.inbox"
)

// Coordination caps, enforced by the server and published here so a
// bridge can reject an oversized body before it reaches the wire.
const (
	// CoordMaxBodyBytes is the largest message body coord.send accepts.
	CoordMaxBodyBytes = 4 << 10
	// CoordMaxUnread is the deepest a run's inbox may get before further
	// sends to it are refused outright.
	CoordMaxUnread = 100
)

// Peer authorization states.
const (
	// CoordPeerActive is a peer the radar currently has this run in file
	// conflict with.
	CoordPeerActive = "active"
	// CoordPeerGrace is a peer whose overlap cleared but whose grace
	// window has not expired yet, so in-flight replies still land.
	CoordPeerGrace = "grace"
)

// CoordPeer is one run the caller is authorized to message. Files are the
// paths both runs touch, empty once the overlap has cleared; ExpiresAt is
// set only in the grace state and is RFC3339.
type CoordPeer struct {
	RunID     string   `json:"run_id"`
	MemberID  string   `json:"member_id"`
	Task      string   `json:"task,omitempty"`
	Files     []string `json:"files,omitempty"`
	State     string   `json:"state"`
	ExpiresAt string   `json:"expires_at,omitempty"`
}

// CoordStatusResult is the result of coord.status: who the caller is,
// exactly the peers it may message, and how many messages are waiting.
type CoordStatusResult struct {
	WireVersion string      `json:"wire_version"`
	RunID       string      `json:"run_id"`
	SessionID   string      `json:"session_id"`
	MemberID    string      `json:"member_id"`
	Task        string      `json:"task,omitempty"`
	Peers       []CoordPeer `json:"peers"`
	Unread      int         `json:"unread"`
}

// CoordSendParams are the params of coord.send. The sender is the socket,
// never a parameter.
type CoordSendParams struct {
	ToRunID string `json:"to_run_id"`
	Body    string `json:"body"`
}

// CoordSendResult is the result of coord.send.
type CoordSendResult struct {
	MessageID string `json:"message_id"`
}

// CoordInboxParams are the params of coord.inbox. AckToken is the token
// of the previous read's batch; omitting it acknowledges nothing, so the
// same batch is delivered again.
type CoordInboxParams struct {
	AckToken string `json:"ack_token,omitempty"`
}

// CoordMessage is one delivered message.
type CoordMessage struct {
	ID        string `json:"id"`
	FromRunID string `json:"from_run_id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// CoordInboxResult is the result of coord.inbox: one batch, oldest first,
// and the token that acknowledges exactly it. An empty inbox returns an
// empty message list and no token.
type CoordInboxResult struct {
	Messages []CoordMessage `json:"messages"`
	AckToken string         `json:"ack_token,omitempty"`
}
