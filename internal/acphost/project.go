package acphost

import (
	"crypto/rand"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/pmezard/go-difflib/difflib"
)

const (
	textFlushInterval = 16 * time.Millisecond
	textFlushBytes    = 2 << 10

	// A tool call update that changes neither status nor title is written
	// only after this much new output or this many skipped updates.
	toolOutputGrowth = 256
	toolSkipLimit    = 10

	maxDiffInput = 1 << 20
)

// projector turns session/update payloads into items. It is not safe for
// concurrent use; the Session serializes every call.
type projector struct {
	emit     func(Item)
	arm      func()
	activity func(kind, target string)

	text  *openText
	tools map[string]*toolState
	plan  []PlanEntry
	usage Usage
	title string
}

type openText struct {
	kind  Kind
	role  string
	id    string
	buf   strings.Builder
	armed bool
}

type toolState struct {
	call          ToolCall
	truncated     bool
	output        string
	emittedStatus string
	emittedTitle  string
	skipped       int
}

func newProjector(emit func(Item), arm func(), activity func(kind, target string)) *projector {
	return &projector{emit: emit, arm: arm, activity: activity, tools: make(map[string]*toolState)}
}

func (p *projector) update(raw json.RawMessage) {
	var head struct {
		Kind string `json:"sessionUpdate"`
	}
	if json.Unmarshal(raw, &head) != nil || !p.project(head.Kind, raw) {
		r, trunc := capRaw(raw, maxContentBytes)
		p.emit(Item{Kind: KindUnknown, Raw: r, Truncated: trunc})
	}
}

// project handles one known update kind and reports false for an unknown
// kind or a payload that does not decode.
func (p *projector) project(kind string, raw json.RawMessage) bool {
	switch kind {
	case "user_message_chunk":
		// Live chunks echo the prompt the host already recorded at turn
		// start; replayed ones are dropped during session/load.
		return true
	case "agent_message_chunk", "agent_thought_chunk":
		var u acp.SessionUpdateAgentMessageChunk
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		k := KindMessage
		if kind == "agent_thought_chunk" {
			k = KindThought
			p.activity("think", "")
		}
		p.chunk(k, "assistant", deref(u.MessageId), u.Content)
	case "tool_call":
		var u acp.SessionUpdateToolCall
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		patch := toolPatch{title: &u.Title, content: u.Content, locations: u.Locations, rawInput: u.RawInput, rawOutput: u.RawOutput, meta: u.Meta}
		if u.Kind != "" {
			k := string(u.Kind)
			patch.kind = &k
		}
		if u.Status != "" {
			s := string(u.Status)
			patch.status = &s
		}
		p.tool(string(u.ToolCallId), patch)
	case "tool_call_update":
		var u acp.SessionToolCallUpdate
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		patch := toolPatch{title: u.Title, content: u.Content, locations: u.Locations, rawInput: u.RawInput, rawOutput: u.RawOutput, meta: u.Meta}
		if u.Kind != nil {
			k := string(*u.Kind)
			patch.kind = &k
		}
		if u.Status != nil {
			s := string(*u.Status)
			patch.status = &s
		}
		p.tool(string(u.ToolCallId), patch)
	case "plan":
		var u acp.SessionUpdatePlan
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		plan := make([]PlanEntry, len(u.Entries))
		for i, e := range u.Entries {
			plan[i] = PlanEntry{Content: e.Content, Priority: string(e.Priority), Status: string(e.Status)}
		}
		if !slices.Equal(plan, p.plan) {
			p.plan = plan
			p.emit(Item{Kind: KindPlan, Plan: plan})
		}
	case "current_mode_update":
		var u acp.SessionCurrentModeUpdate
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		p.emit(Item{Kind: KindModeChange, Mode: string(u.CurrentModeId)})
	case "config_option_update":
		var u struct {
			ConfigOptions json.RawMessage `json:"configOptions"`
		}
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		p.emit(Item{Kind: KindConfigOptions, ConfigOptions: u.ConfigOptions})
	case "available_commands_update":
		var u struct {
			AvailableCommands json.RawMessage `json:"availableCommands"`
		}
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		p.emit(Item{Kind: KindCommands, Commands: u.AvailableCommands})
	case "usage_update":
		var u acp.SessionUsageUpdate
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		usage := Usage{Used: int64(u.Used), Size: int64(u.Size)}
		if u.Cost != nil {
			usage.Cost, usage.Currency = &u.Cost.Amount, u.Cost.Currency
		}
		if usage.Used != p.usage.Used || usage.Size != p.usage.Size || usage.Cost != nil {
			p.usage = usage
			p.emit(Item{Kind: KindUsage, Usage: &usage})
		}
	case "session_info_update":
		var u acp.SessionSessionInfoUpdate
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		if u.Title != nil && *u.Title != p.title {
			p.title = *u.Title
			p.emit(Item{Kind: KindSessionInfo, Title: p.title})
		}
	default:
		return false
	}
	return true
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// chunk appends streamed text to the open message, starting a new one when
// the kind, role or message id changes.
func (p *projector) chunk(kind Kind, role, id string, block acp.ContentBlock) {
	if t := p.text; t != nil && (t.kind != kind || t.role != role || (id != "" && id != t.id)) {
		p.closeText()
	}
	if p.text == nil {
		if id == "" {
			id = rand.Text()
		}
		p.text = &openText{kind: kind, role: role, id: id}
	}
	t := p.text
	if block.Text == nil {
		p.flushText()
		p.emit(Item{Kind: kind, Message: &Message{Role: role, MessageID: t.id, Attachments: []Content{contentRef(block)}}})
		return
	}
	t.buf.WriteString(block.Text.Text)
	if t.buf.Len() >= textFlushBytes {
		p.flushText()
		return
	}
	if !t.armed {
		t.armed = true
		p.arm()
	}
}

func (p *projector) flushText() {
	t := p.text
	if t == nil {
		return
	}
	t.armed = false
	s := t.buf.String()
	t.buf.Reset()
	for len(s) > 0 {
		seg, _ := cutTail(s, maxTextSegment)
		if seg == "" {
			// No rune boundary in reach: the text is not UTF-8.
			seg = s[:maxTextSegment]
		}
		s = s[len(seg):]
		p.emit(Item{Kind: t.kind, Message: &Message{Role: t.role, MessageID: t.id, Text: seg}})
	}
}

func (p *projector) closeText() {
	t := p.text
	if t == nil {
		return
	}
	s := t.buf.String()
	t.buf.Reset()
	if len(s) > maxTextSegment {
		t.buf.WriteString(s)
		p.flushText()
		s = ""
	}
	p.text = nil
	p.emit(Item{Kind: t.kind, Message: &Message{Role: t.role, MessageID: t.id, Text: s, Complete: true}})
}

type toolPatch struct {
	title     *string
	kind      *string
	status    *string
	content   []acp.ToolCallContent
	locations []acp.ToolCallLocation
	rawInput  any
	rawOutput any
	meta      map[string]any
}

// tool merges an update into the tool call's state; absent fields keep
// their previous value, content and locations are replaced when present.
func (p *projector) tool(id string, patch toolPatch) {
	st, known := p.tools[id]
	if !known {
		p.closeText()
		st = &toolState{call: ToolCall{ID: id}}
		p.tools[id] = st
	}
	c := &st.call
	if patch.title != nil {
		var cut bool
		c.Title, cut = cutTail(*patch.title, maxShrunkString)
		st.truncated = st.truncated || cut
	}
	if patch.kind != nil {
		c.ToolKind = *patch.kind
	}
	if patch.status != nil {
		c.Status = *patch.status
	}
	if patch.locations != nil {
		c.Locations = make([]Location, len(patch.locations))
		for i, l := range patch.locations {
			c.Locations[i] = Location{Path: l.Path, Line: l.Line}
		}
	}
	if patch.content != nil {
		var cut bool
		c.Content, c.Diffs, cut = toolContent(patch.content)
		st.truncated = st.truncated || cut
	}
	if patch.rawInput != nil {
		var cut bool
		c.RawInput, cut = capRaw(patch.rawInput, maxRawBytes)
		st.truncated = st.truncated || cut
	}
	if patch.rawOutput != nil {
		var cut bool
		c.RawOutput, cut = capRaw(patch.rawOutput, maxRawBytes)
		st.truncated = st.truncated || cut
	}
	for _, key := range []string{"terminal_output", "terminal_output_delta"} {
		if out, ok := patch.meta[key].(map[string]any); ok {
			if data, ok := out["data"].(string); ok {
				st.output += data
				c.OutputBytes += int64(len(data))
			}
		}
	}
	if exit, ok := patch.meta["terminal_exit"].(map[string]any); ok {
		if code, ok := exit["exit_code"].(float64); ok {
			n := int(code)
			c.ExitCode = &n
		}
	}

	final := c.Status == string(acp.ToolCallStatusCompleted) || c.Status == string(acp.ToolCallStatusFailed)
	if !known || final || c.Status != st.emittedStatus || c.Title != st.emittedTitle ||
		len(st.output) >= toolOutputGrowth || st.skipped+1 >= toolSkipLimit {
		p.emitTool(st)
		if !final {
			p.activity(c.ToolKind, toolTarget(c))
		}
		return
	}
	st.skipped++
}

func (p *projector) emitTool(st *toolState) {
	snap := st.call
	snap.Locations = slices.Clone(snap.Locations)
	snap.Content = slices.Clone(snap.Content)
	snap.Diffs = slices.Clone(snap.Diffs)
	var cut bool
	snap.Output, cut = keepTail(st.output, maxOutputDelta)
	st.output = ""
	st.skipped = 0
	st.emittedStatus, st.emittedTitle = snap.Status, snap.Title
	p.emit(Item{Kind: KindToolCall, ToolCall: &snap, Truncated: st.truncated || cut})
}

// endTurn closes the open message and writes every tool call update that
// was held back.
func (p *projector) endTurn() {
	p.closeText()
	for _, id := range slices.Sorted(maps.Keys(p.tools)) {
		if st := p.tools[id]; st.skipped > 0 || st.output != "" {
			p.emitTool(st)
		}
	}
	clear(p.tools)
}

func toolContent(cs []acp.ToolCallContent) ([]Content, []Diff, bool) {
	var content []Content
	var diffs []Diff
	var truncated bool
	budget := maxContentBytes
	for _, c := range cs {
		switch {
		case c.Content != nil:
			ref := contentRef(c.Content.Content)
			if ref.Type == "text" {
				var cut bool
				ref.Text, cut = cutTail(ref.Text, max(budget, 0))
				budget -= len(ref.Text)
				truncated = truncated || cut
			}
			content = append(content, ref)
		case c.Diff != nil:
			patch, cut := unifiedPatch(c.Diff.Path, c.Diff.OldText, c.Diff.NewText)
			diffs = append(diffs, Diff{Path: c.Diff.Path, Patch: patch})
			truncated = truncated || cut
		case c.Terminal != nil:
			content = append(content, Content{Type: "terminal", TerminalID: c.Terminal.TerminalId})
		}
	}
	return content, diffs, truncated
}

func contentRef(b acp.ContentBlock) Content {
	switch {
	case b.Text != nil:
		return Content{Type: "text", Text: b.Text.Text}
	case b.Image != nil:
		return Content{Type: "image", MimeType: b.Image.MimeType, URI: deref(b.Image.Uri)}
	case b.Audio != nil:
		return Content{Type: "audio", MimeType: b.Audio.MimeType}
	case b.ResourceLink != nil:
		return Content{Type: "resource_link", URI: b.ResourceLink.Uri, MimeType: deref(b.ResourceLink.MimeType)}
	case b.Resource != nil:
		r := b.Resource.Resource
		switch {
		case r.TextResourceContents != nil:
			return Content{Type: "resource", URI: r.TextResourceContents.Uri, MimeType: deref(r.TextResourceContents.MimeType)}
		case r.BlobResourceContents != nil:
			return Content{Type: "resource", URI: r.BlobResourceContents.Uri, MimeType: deref(r.BlobResourceContents.MimeType)}
		}
	}
	return Content{Type: "unknown"}
}

// unifiedPatch renders an ACP {path, oldText, newText} diff as a unified
// patch; a nil oldText is a new file.
func unifiedPatch(path string, oldText *string, newText string) (string, bool) {
	from := "a/" + strings.TrimPrefix(path, "/")
	var a []string
	if oldText == nil {
		from = "/dev/null"
	} else {
		a = difflib.SplitLines(*oldText)
	}
	header := "--- " + from + "\n+++ b/" + strings.TrimPrefix(path, "/") + "\n"
	if len(newText)+len(deref(oldText)) > maxDiffInput {
		return header, true
	}
	patch, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        a,
		B:        difflib.SplitLines(newText),
		FromFile: from,
		ToFile:   "b/" + strings.TrimPrefix(path, "/"),
		Context:  3,
	})
	if err != nil {
		return header, true
	}
	return cutTail(patch, maxDiffBytes)
}

func ToolVerb(kind string) string {
	switch kind {
	case "read":
		return "Reading"
	case "edit":
		return "Editing"
	case "delete":
		return "Deleting"
	case "move":
		return "Moving"
	case "search":
		return "Searching"
	case "execute":
		return "Running"
	case "think":
		return "Thinking"
	case "fetch":
		return "Fetching"
	case "switch_mode":
		return "Switching mode"
	}
	return "Using"
}

func toolTarget(c *ToolCall) string {
	if len(c.Locations) > 0 {
		return c.Locations[0].Path
	}
	return c.Title
}
