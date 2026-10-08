package acphost

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind names what an Item records. Clients switch on it; unknown kinds must
// be skipped, not rejected.
type Kind string

const (
	KindMessage       Kind = "message"
	KindThought       Kind = "thought"
	KindToolCall      Kind = "tool_call"
	KindPlan          Kind = "plan"
	KindRequest       Kind = "request"
	KindModeChange    Kind = "mode_change"
	KindConfigOptions Kind = "config_options"
	KindCommands      Kind = "commands"
	KindUsage         Kind = "usage"
	KindAuthStatus    Kind = "auth_status"
	KindSessionInfo   Kind = "session_info"
	KindNotice        Kind = "notice"
	KindTurnStart     Kind = "turn_start"
	KindTurnEnd       Kind = "turn_end"
	KindReset         Kind = "reset"
	KindUnknown       Kind = "unknown"
)

const (
	// MaxItemBytes caps one encoded item. Larger payloads are cut and the
	// item is flagged Truncated.
	MaxItemBytes = 256 << 10

	maxTextSegment  = 64 << 10
	maxOutputDelta  = 64 << 10
	maxDiffBytes    = 64 << 10
	maxContentBytes = 32 << 10
	maxRawBytes     = 16 << 10
	maxShrunkString = 4 << 10
	// Room image uploads allow eight attachments; their generated message IDs
	// and single-digit indices fit comfortably in this bounded reference.
	maxRoomImages   = 8
	maxRoomImageURI = 128
)

// Item is one entry of a run's session item log. Seq is per run and strictly
// increasing; Epoch increases at every Reset. Exactly one payload field is
// set, chosen by Kind.
//
// Messages and thoughts are written as segments: every item carries the text
// appended since the previous segment with the same MessageID, and the last
// segment has Complete set. Tool calls are written as merged snapshots that
// replace the previous snapshot with the same ID, except Output, which is
// the command output appended since the previous snapshot. Requests are
// written once when opened and again when answered or cancelled.
type Item struct {
	Seq       int64     `json:"seq"`
	Epoch     int64     `json:"epoch"`
	Time      time.Time `json:"time"`
	Turn      int64     `json:"turn"`
	Kind      Kind      `json:"kind"`
	Truncated bool      `json:"truncated,omitempty"`

	Message       *Message        `json:"message,omitempty"`
	ToolCall      *ToolCall       `json:"tool_call,omitempty"`
	Plan          []PlanEntry     `json:"plan,omitempty"`
	Request       *Request        `json:"request,omitempty"`
	Mode          string          `json:"mode,omitempty"`
	ConfigOptions json.RawMessage `json:"config_options,omitempty"`
	Commands      json.RawMessage `json:"commands,omitempty"`
	Usage         *Usage          `json:"usage,omitempty"`
	Auth          json.RawMessage `json:"auth,omitempty"`
	Title         string          `json:"title,omitempty"`
	Notice        *Notice         `json:"notice,omitempty"`
	StopReason    string          `json:"stop_reason,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}

// Message is one segment of a user message, assistant message, or thought.
type Message struct {
	Role        string    `json:"role"`
	MessageID   string    `json:"message_id"`
	Text        string    `json:"text"`
	Attachments []Content `json:"attachments,omitempty"`
	Complete    bool      `json:"complete,omitempty"`
}

// Content is a reference to a non-text content block, or the text of a
// text block inside a tool call. Binary data is never stored.
type Content struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	MimeType   string `json:"mime_type,omitempty"`
	URI        string `json:"uri,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
}

type ToolCall struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	ToolKind    string          `json:"tool_kind,omitempty"`
	Status      string          `json:"status,omitempty"`
	Locations   []Location      `json:"locations,omitempty"`
	Content     []Content       `json:"content,omitempty"`
	Diffs       []Diff          `json:"diffs,omitempty"`
	RawInput    json.RawMessage `json:"raw_input,omitempty"`
	RawOutput   json.RawMessage `json:"raw_output,omitempty"`
	Output      string          `json:"output,omitempty"`
	OutputBytes int64           `json:"output_bytes,omitempty"`
	ExitCode    *int            `json:"exit_code,omitempty"`
}

type Location struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// Diff is one file change as a unified patch.
type Diff struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

// PlanEntry is one step of the agent's plan. A plan item always carries the
// whole plan.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

const (
	RequestPermission = "permission"
	RequestQuestion   = "question"
	RequestLink       = "link"

	RequestPending   = "pending"
	RequestAnswered  = "answered"
	RequestCancelled = "cancelled"
)

// Request is something the agent is waiting on a person for. ACP requests
// are not ordered against session/update notifications, so a Request item,
// and the tool call snapshot a permission request carries, can be logged
// before updates the agent sent earlier.
type Request struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Options    []Option        `json:"options,omitempty"`
	Schema     json.RawMessage `json:"schema,omitempty"`
	URL        string          `json:"url,omitempty"`
	Status     string          `json:"status"`
	Answer     string          `json:"answer,omitempty"`
}

// Option is one answer a person can pick. Kind is the ACP permission option
// kind (allow_once, allow_always, reject_once, reject_always) or empty.
type Option struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// Usage is the agent's context window and cost report.
type Usage struct {
	Used     int64    `json:"used"`
	Size     int64    `json:"size"`
	Cost     *float64 `json:"cost,omitempty"`
	Currency string   `json:"currency,omitempty"`
}

// Notice is a host- or agent-reported event that is not part of the
// conversation.
type Notice struct {
	Severity    string `json:"severity"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

func (it *Item) encode() ([]byte, error) {
	b, err := json.Marshal(it)
	if err != nil || len(b) <= MaxItemBytes {
		return b, err
	}
	it.shrink()
	return json.Marshal(it)
}

// shrink drops bulk payloads so the item fits MaxItemBytes while keeping
// identity fields clients upsert by.
func (it *Item) shrink() {
	it.Truncated = true
	it.Raw = nil
	it.ConfigOptions = nil
	it.Commands = nil
	it.Auth = nil
	if len(it.Plan) > 0 {
		for i := range it.Plan {
			it.Plan[i].Content, _ = cutTail(it.Plan[i].Content, 256)
		}
		if len(it.Plan) > 256 {
			it.Plan = it.Plan[:256]
		}
	}
	if m := it.Message; m != nil {
		m.Text, _ = cutTail(m.Text, maxShrunkString)
		m.Attachments = roomImageRefs(m.Attachments)
	}
	if r := it.Request; r != nil {
		r.Title, _ = cutTail(r.Title, maxShrunkString)
		r.Schema = nil
		if len(r.Options) > 32 {
			r.Options = r.Options[:32]
		}
	}
	if tc := it.ToolCall; tc != nil {
		tc.Title, _ = cutTail(tc.Title, maxShrunkString)
		tc.Output, _ = keepTail(tc.Output, maxShrunkString)
		tc.RawInput = nil
		tc.RawOutput = nil
		tc.Content = nil
		if len(tc.Locations) > 32 {
			tc.Locations = tc.Locations[:32]
		}
		for i := range tc.Diffs {
			tc.Diffs[i].Patch, _ = cutTail(tc.Diffs[i].Patch, maxShrunkString)
		}
		if len(tc.Diffs) > 16 {
			tc.Diffs = tc.Diffs[:16]
		}
	}
}

// roomImageRefs keeps only short authenticated-preview identities when bulk
// content must be discarded. Never retain arbitrary URLs or other payload fields.
func roomImageRefs(contents []Content) []Content {
	var refs []Content
	for _, content := range contents {
		if content.Type != "image" || len(content.URI) > maxRoomImageURI {
			continue
		}
		switch content.MimeType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			continue
		}
		path, ok := strings.CutPrefix(content.URI, "aether://room/")
		if !ok {
			continue
		}
		message, index, ok := strings.Cut(path, "/")
		if !ok || len(index) != 1 || index[0] < '0' || index[0] >= '0'+maxRoomImages {
			continue
		}
		id, err := url.PathUnescape(message)
		if err != nil || id == "" || strings.ContainsAny(id, "/\\?#") || !utf8.ValidString(id) {
			continue
		}
		safe := true
		for _, r := range id {
			if r <= ' ' || r == 0x7f {
				safe = false
				break
			}
		}
		if !safe {
			continue
		}
		if refs == nil {
			refs = make([]Content, 0, min(len(contents), maxRoomImages))
		}
		refs = append(refs, Content{Type: "image", MimeType: content.MimeType, URI: content.URI})
		if len(refs) == maxRoomImages {
			break
		}
	}
	return refs
}

// cutTail keeps the first n bytes of s on a rune boundary.
func cutTail(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// keepTail keeps the last n bytes of s on a rune boundary.
func keepTail(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:], true
}

// capRaw re-encodes v and keeps it only when it fits n bytes; a larger value
// becomes a JSON string holding its first n bytes.
func capRaw(v any, n int) (json.RawMessage, bool) {
	if v == nil {
		return nil, false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, true
	}
	if len(b) <= n {
		return b, false
	}
	cut, _ := cutTail(string(b), n)
	s, _ := json.Marshal(cut)
	return s, true
}

// Wire encodes the item for a viewer within limit bytes. A larger item is cut
// to its identity fields, which clients upsert by, and reports truncated.
func (it Item) Wire(limit int) (b []byte, truncated bool, err error) {
	if b, err = json.Marshal(it); err != nil || len(b) <= limit {
		return b, false, err
	}
	var cut Item
	if err = json.Unmarshal(b, &cut); err != nil {
		return nil, false, err
	}
	cut.shrink()
	if b, err = json.Marshal(cut); err != nil || len(b) <= limit {
		return b, true, err
	}
	brief := Item{Seq: it.Seq, Epoch: it.Epoch, Time: it.Time, Turn: it.Turn, Kind: it.Kind, Truncated: true,
		Mode: cut.Mode, StopReason: cut.StopReason}
	if m := cut.Message; m != nil {
		brief.Message = &Message{Role: m.Role, MessageID: m.MessageID, Complete: m.Complete, Attachments: m.Attachments}
	}
	if tc := cut.ToolCall; tc != nil {
		brief.ToolCall = &ToolCall{ID: tc.ID, ToolKind: tc.ToolKind, Status: tc.Status, ExitCode: tc.ExitCode}
	}
	if r := cut.Request; r != nil {
		brief.Request = &Request{ID: r.ID, Kind: r.Kind, ToolCallID: r.ToolCallID, Status: r.Status, Answer: r.Answer}
	}
	b, err = json.Marshal(brief)
	// If a caller's frame is too small even for bounded references, shed only
	// the references that cannot fit rather than returning an oversized frame.
	for err == nil && len(b) > limit && brief.Message != nil && len(brief.Message.Attachments) > 0 {
		brief.Message.Attachments = brief.Message.Attachments[:len(brief.Message.Attachments)-1]
		b, err = json.Marshal(brief)
	}
	return b, true, err
}
