// Package acpkeeper keeps an enhanced run's ACP adapter attached across a
// server restart. A keeper process inside the run container owns the
// adapter's stdio and serves it on a container-local unix socket; each server
// process attaches to it through a short-lived bridge exec, and a newer
// server takes the adapter over without a new session, prompt or turn.
//
// The socket carries one JSON frame per line. The first frame an attacher
// sends is a hello; the keeper answers welcome, with the state the attacher
// adopts, or refused. Every agent-to-client ACP message is a numbered rpc
// frame that the keeper retains until the attacher acks it; client-to-agent
// messages are unnumbered rpc frames.
//
// A terminal keeper speaks the same hello on the same kind of socket for a
// command it runs on a PTY it owns. Its output frames carry the PTY's bytes
// numbered by offset; a hello names the offset to replay from.
package acpkeeper

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const (
	// Command is the staged binary's keeper subcommand.
	Command = "acp-keeper"
	// Version is the frame protocol this build speaks. A keeper refuses a
	// hello for any other version, which sends the server back to
	// stopping the adapter and restoring its session.
	Version = 1
	// AdoptMethod is the request an attacher sends in place of the
	// session/prompt an earlier attacher left unanswered. The keeper answers
	// it with that prompt's answer and forwards nothing to the agent.
	AdoptMethod = "_aether/keeper/adopt_prompt"
	// HandledMethod is a notification a Client inserts into the stream it
	// hands acphost, never sent on the wire. acphost reports it handled once
	// every notification before it is, and the Client acks only that far.
	HandledMethod = "_aether/keeper/handled"
	// SocketRoot holds each keeper's socket directory. Only root may enter
	// it: the agent's user must not reach the socket.
	SocketRoot = "/run/aether-acp"

	// maxLine bounds one frame. The SDK reads agent messages of up to 10
	// MiB; a prompt with several images can be larger.
	maxLine = 64 << 20
	// stderrTail is how much of the adapter's stderr the keeper keeps.
	stderrTail = 4 << 10
	// maxGenStep bounds how far a hello's generation may run ahead of the
	// newest the keeper saw. A server persists each generation before its
	// hello, so only a failed hello skips one.
	maxGenStep = 1 << 16
)

// Frame types.
const (
	frameHello     = "hello"
	frameWelcome   = "welcome"
	frameRefused   = "refused"
	frameRPC       = "rpc"
	frameAck       = "ack"
	frameDetach    = "detach"
	frameBye       = "bye"
	frameClose     = "close"
	frameExit      = "exit"
	framePreempted = "preempted"
	frameOutput    = "out"
	frameInput     = "in"
	frameResize    = "resize"
)

// Refusal codes.
const (
	// RefusedToken: the hello did not carry the keeper's token.
	RefusedToken = "token"
	// RefusedVersion: the keeper speaks another protocol version.
	RefusedVersion = "version"
	// RefusedStale: a server generation at least as new already attached,
	// or the hello's generation is implausibly far ahead.
	RefusedStale = "stale"
	// RefusedOverflow: the keeper dropped agent output it could not retain
	// while no server read it, so attaching would lose some.
	RefusedOverflow = "overflow"
	// RefusedExited: the adapter exited.
	RefusedExited = "exited"
)

type frame struct {
	T   string          `json:"t"`
	Seq uint64          `json:"seq,omitempty"`
	M   json.RawMessage `json:"m,omitempty"`
	// Tag names what a client-to-agent request delivers, such as a room
	// message id; the keeper reports the tags of requests it forwarded for
	// an attacher that is gone.
	Tag string `json:"tag,omitempty"`
	// Turn is the item log's turn a session/prompt starts.
	Turn int64 `json:"turn,omitempty"`

	V     int    `json:"v,omitempty"`
	Gen   uint64 `json:"gen,omitempty"`
	Token string `json:"token,omitempty"`
	Ack   uint64 `json:"ack,omitempty"`

	State *State `json:"state,omitempty"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
	Exit  *Exit  `json:"exit,omitempty"`

	Data []byte `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// State is what a welcome hands the attacher: what the adapter answered the
// first attacher's initialize and the latest session/new, session/load or
// session/resume, and the work earlier attachers left in flight.
type State struct {
	Initialize json.RawMessage `json:"initialize,omitempty"`
	Session    json.RawMessage `json:"session,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	// Prompt is the session/prompt an earlier attacher sent whose answer no
	// attacher acked; AdoptMethod takes it over.
	Prompt *Prompt `json:"prompt,omitempty"`
	// Delivered are the tags of requests earlier attachers sent that reached
	// the agent and whose answer no attacher acked.
	Delivered []string `json:"delivered,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	// Term is a terminal keeper's state.
	Term *TermState `json:"term,omitempty"`
}

// TermState is the output a terminal keeper retains, by byte offset since
// the terminal started, and its geometry.
type TermState struct {
	// Start is the offset of the oldest retained byte, End the offset after
	// the newest.
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
	Cols  uint16 `json:"cols"`
	Rows  uint16 `json:"rows"`
}

// Prompt is a session/prompt in flight when its attacher went away. Turn is
// the item log turn it started: an attacher whose log shows another turn
// open must not adopt it.
type Prompt struct {
	Turn int64 `json:"turn"`
}

// Exit is how the adapter ended.
type Exit struct {
	Code   int    `json:"code"`
	Stderr string `json:"stderr,omitempty"`
}

// RefusedError is a keeper's refusal of a hello.
type RefusedError struct {
	Code    string
	Message string
	Exit    *Exit
}

func (e *RefusedError) Error() string {
	if e.Message == "" {
		return "acpkeeper: attach refused: " + e.Code
	}
	return fmt.Sprintf("acpkeeper: attach refused: %s: %s", e.Code, e.Message)
}

// ErrNothingToAdopt answers AdoptMethod when no earlier prompt is in flight.
var ErrNothingToAdopt = errors.New("acpkeeper: no prompt in flight to adopt")

// rpcHead is the part of an ACP message the keeper routes on.
type rpcHead struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		SessionID string `json:"sessionId"`
	} `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (h rpcHead) request() bool  { return h.Method != "" && present(h.ID) }
func (h rpcHead) response() bool { return h.Method == "" && present(h.ID) }

func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

// withID returns line with its JSON-RPC id replaced.
func withID(line []byte, id json.RawMessage) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	m["id"] = id
	return json.Marshal(m)
}

// ServeArgv is the command that runs bin, the staged binary, as the keeper
// of adapter listening on socket. With user, a "uid:gid", the keeper starts
// adapter as that user.
func ServeArgv(bin, socket, user string, adapter []string) []string {
	argv := []string{bin, Command, "serve", socket}
	if user != "" {
		argv = append(argv, "--user", user)
	}
	return append(append(argv, "--"), adapter...)
}

// TermArgv is the command that runs bin as the keeper of a terminal of cols
// by rows that runs argv, listening on socket. With user, a "uid:gid", the
// keeper starts argv as that user.
func TermArgv(bin, socket, user string, cols, rows uint, argv []string) []string {
	out := []string{bin, Command, "term", socket, strconv.FormatUint(uint64(cols), 10) + "x" + strconv.FormatUint(uint64(rows), 10)}
	if user != "" {
		out = append(out, "--user", user)
	}
	return append(append(out, "--"), argv...)
}

// AttachArgv is the command that bridges its stdio to the keeper at socket.
func AttachArgv(bin, socket string) []string {
	return []string{bin, Command, "attach", socket}
}
