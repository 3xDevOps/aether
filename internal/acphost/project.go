package acphost

import (
	"cmp"
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

	// The newest tool calls stay known past the end of their turn, so a
	// late update merges into its previous snapshot.
	maxKeptTools = 256
)

// projector turns session/update payloads into items. It is not safe for
// concurrent use; the Session serializes every call.
type projector struct {
	emit     func(Item)
	arm      func()
	activity func(kind, target string)

	text  *openText
	tools map[string]*toolState
	clock uint64
	// plans holds the last plan of the main agent ("") and of each subagent,
	// by the ID of the tool call that launched it.
	plans map[string][]PlanEntry
	// labels holds what the announcements of each child session said, by
	// the launch id Aether made up for it.
	labels map[string]subagentLabels
	usage  Usage
	title  string
}

type subagentLabels struct{ title, description, prompt, status string }

type openText struct {
	kind   Kind
	role   string
	id     string
	parent string
	buf    strings.Builder
	armed  bool
}

type toolState struct {
	call          ToolCall
	truncated     bool
	output        string
	emittedStatus string
	emittedTitle  string
	skipped       int
	updated       uint64
	// ended is set when a turn ends.
	ended bool
}

func newProjector(emit func(Item), arm func(), activity func(kind, target string)) *projector {
	return &projector{emit: emit, arm: arm, activity: activity, tools: make(map[string]*toolState), plans: make(map[string][]PlanEntry),
		labels: make(map[string]subagentLabels)}
}

// update projects an update of the main agent's session, or with parent set
// of the subagent session that parent launched.
func (p *projector) update(parent string, raw json.RawMessage) {
	var head struct {
		Kind string `json:"sessionUpdate"`
	}
	if json.Unmarshal(raw, &head) != nil || !p.project(head.Kind, parent, raw) {
		r, trunc := capRaw(raw, maxContentBytes)
		p.emit(Item{Kind: KindUnknown, Raw: r, Truncated: trunc})
	}
}

// project handles one known update kind and reports false for an unknown
// kind or a payload that does not decode.
func (p *projector) project(kind, parent string, raw json.RawMessage) bool {
	if parent != "" {
		switch kind {
		case "usage_update", "session_info_update", "current_mode_update", "config_option_update", "available_commands_update":
			// The run's usage, title, mode, options and commands are the
			// main agent's.
			return true
		}
	}
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
		claude, _ := claudeMeta(u.Meta)
		p.chunk(k, "assistant", deref(u.MessageId), cmp.Or(parent, claude), u.Content)
	case "tool_call":
		var u acp.SessionUpdateToolCall
		if json.Unmarshal(raw, &u) != nil {
			return false
		}
		patch := toolPatch{opens: true, parent: parent, title: &u.Title, content: u.Content, locations: u.Locations, rawInput: u.RawInput, rawOutput: u.RawOutput, meta: u.Meta}
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
		patch := toolPatch{parent: parent, title: u.Title, content: u.Content, locations: u.Locations, rawInput: u.RawInput, rawOutput: u.RawOutput, meta: u.Meta}
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
		claude, _ := claudeMeta(u.Meta)
		owner := cmp.Or(parent, claude)
		if !slices.Equal(plan, p.plans[owner]) {
			p.plans[owner] = plan
			p.emit(Item{Kind: KindPlan, Plan: plan, PlanParentID: owner})
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
	case "subagent_spawned", "subagent_state_update", "subagent_update":
		c, ok := parseSubagent(raw)
		if !ok {
			return false
		}
		p.subagent(parent, c)
	default:
		return false
	}
	return true
}

// subagent records a child session's announcement or state change on the
// tool call that launched it. Cursor's own task call only gets its label;
// for a launch id Aether made up, the announcements are the whole call.
func (p *projector) subagent(parent string, c subagentChild) {
	if c.ToolCall {
		if c.Announces {
			kind, _ := cutTail(deref(c.Type), maxLinkBytes)
			p.tool(c.Launch, toolPatch{parent: parent, subagent: &Subagent{Type: kind}})
		}
		return
	}
	l, ok := p.label(c)
	if !ok {
		return
	}
	in := map[string]any{}
	if l.description != "" {
		in["description"] = l.description
	}
	if l.prompt != "" {
		in["prompt"] = l.prompt
	}
	label, _ := cutTail(l.title, maxLinkBytes)
	kind := string(acp.ToolKindThink)
	p.tool(c.Launch, toolPatch{parent: parent, title: &l.title, kind: &kind, status: &l.status, rawInput: in, subagent: &Subagent{Description: label}})
}

// label merges what an update says about a child with a launch id Aether
// made up into the child's labels. It reports false for the agent's own
// launch call and for a child no announcement introduced.
func (p *projector) label(c subagentChild) (subagentLabels, bool) {
	l, known := p.labels[c.Launch]
	if c.ToolCall || !known && !c.Announces {
		return l, false
	}
	if c.Title != nil {
		l.title, _ = cutTail(*c.Title, maxShrunkString)
	}
	if c.Description != nil {
		l.description, _ = cutTail(*c.Description, maxShrunkString)
	}
	if c.Prompt != nil {
		l.prompt, _ = cutTail(*c.Prompt, maxRawBytes)
	}
	l.status = cmp.Or(c.Status, l.status)
	p.labels[c.Launch] = l
	return l, true
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// claudeMeta reads the subagent link and tool name Claude's adapter stamps
// on an update. Other agents send neither.
func claudeMeta(meta map[string]any) (parent, toolName string) {
	cc, _ := meta["claudeCode"].(map[string]any)
	parent, _ = cc["parentToolUseId"].(string)
	toolName, _ = cc["toolName"].(string)
	if len(parent) > maxLinkBytes {
		parent = ""
	}
	if len(toolName) > maxLinkBytes {
		toolName = ""
	}
	return parent, toolName
}

// launchedTask is the id of the background task a Claude Code tool result
// reports starting: a backgrounded command's backgroundTaskId, or the taskId
// or agentId of a workflow or subagent launched in the background.
func launchedTask(meta map[string]any) string {
	cc, _ := meta["claudeCode"].(map[string]any)
	r, _ := cc["toolResponse"].(map[string]any)
	id, _ := r["backgroundTaskId"].(string)
	if status, _ := r["status"].(string); id == "" && status == "async_launched" {
		if id, _ = r["taskId"].(string); id == "" {
			id, _ = r["agentId"].(string)
		}
	}
	if len(id) > maxLinkBytes {
		return ""
	}
	return id
}

// setTask records task on a tool call the projector still knows, so a later
// update does not bring back its earlier state.
func (p *projector) setTask(id string, task *ToolTask) {
	if st, ok := p.tools[id]; ok {
		st.call.Task = task
	}
}

// chunk appends streamed text to the open message, starting a new one when
// the kind, role, message id or subagent changes.
func (p *projector) chunk(kind Kind, role, id, parent string, block acp.ContentBlock) {
	if t := p.text; t != nil && (t.kind != kind || t.role != role || t.parent != parent || (id != "" && id != t.id)) {
		p.closeText()
	}
	if p.text == nil {
		if id == "" {
			id = rand.Text()
		}
		p.text = &openText{kind: kind, role: role, id: id, parent: parent}
	}
	t := p.text
	if block.Text == nil {
		p.flushText()
		p.emit(Item{Kind: kind, Message: &Message{Role: role, MessageID: t.id, ParentID: t.parent, Attachments: []Content{contentRef(block)}}})
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
		p.emit(Item{Kind: t.kind, Message: &Message{Role: t.role, MessageID: t.id, ParentID: t.parent, Text: seg}})
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
	p.emit(Item{Kind: t.kind, Message: &Message{Role: t.role, MessageID: t.id, ParentID: t.parent, Text: s, Complete: true}})
}

type toolPatch struct {
	// opens is set for a tool_call, which starts over an ID an earlier turn
	// used.
	opens bool
	// parent is the launch id of the subagent session the update came from.
	parent    string
	title     *string
	kind      *string
	status    *string
	content   []acp.ToolCallContent
	locations []acp.ToolCallLocation
	rawInput  any
	rawOutput any
	meta      map[string]any
	subagent  *Subagent
}

// tool merges an update into the tool call's state; absent fields keep
// their previous value, content and locations are replaced when present.
func (p *projector) tool(id string, patch toolPatch) {
	st, known := p.tools[id]
	if known && patch.opens && st.ended {
		known = false
		delete(p.plans, id)
	}
	if !known {
		p.closeText()
		st = &toolState{call: ToolCall{ID: id}}
		p.tools[id] = st
	}
	p.clock++
	st.updated = p.clock
	c := &st.call
	claude, toolName := claudeMeta(patch.meta)
	// A later update of a subagent's tool call can omit the link.
	if parent := cmp.Or(patch.parent, claude); parent != "" && parent != id {
		c.ParentID = parent
	}
	if toolName != "" && c.ToolName == "" {
		c.ToolName = toolName
	}
	if task := launchedTask(patch.meta); task != "" && c.Task == nil {
		c.Task = &ToolTask{ID: task, Status: "running"}
	}
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
	sub := c.Subagent
	if in, ok := patch.rawInput.(map[string]any); ok {
		description, _ := in["description"].(string)
		kind, _ := in["subagent_type"].(string)
		for _, key := range []string{"agent", "agent_type"} {
			if kind == "" {
				kind, _ = in[key].(string)
			}
		}
		prompt, _ := in["prompt"].(string)
		if prompt == "" {
			prompt, _ = in["task"].(string)
		}
		named := c.ToolName == "Agent" || c.ToolName == "Task"
		if (named && (description != "" || kind != "")) || (kind != "" && (description != "" || prompt != "")) {
			description, _ = cutTail(description, maxLinkBytes)
			kind, _ = cutTail(kind, maxLinkBytes)
			c.Subagent = &Subagent{Description: description, Type: kind}
		}
	}
	if patch.subagent != nil {
		c.Subagent = patch.subagent
	}
	relabelled := (sub == nil) != (c.Subagent == nil) || sub != nil && *sub != *c.Subagent
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
	// No later endTurn writes what a call that outlived its turn holds back.
	if !known || final || st.ended || relabelled || c.Status != st.emittedStatus || c.Title != st.emittedTitle ||
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

// toolsOpen reports whether a tool call of the current turn is pending or
// running.
func (p *projector) toolsOpen() bool {
	for _, st := range p.tools {
		if !st.ended && (st.call.Status == "pending" || st.call.Status == "in_progress") {
			return true
		}
	}
	return false
}

// endTurn closes the open message and writes every tool call update that
// was held back. Finished calls are forgotten; the most recently updated
// in-progress ones stay known, because a background subagent reports on its
// tool calls after the turn that started them.
func (p *projector) endTurn() {
	p.closeText()
	for _, id := range slices.Sorted(maps.Keys(p.tools)) {
		st := p.tools[id]
		if st.skipped > 0 || st.output != "" {
			p.emitTool(st)
		}
		if st.call.Status == string(acp.ToolCallStatusCompleted) || st.call.Status == string(acp.ToolCallStatusFailed) {
			delete(p.tools, id)
			delete(p.plans, id)
			continue
		}
		st.ended = true
	}
	if drop := len(p.tools) - maxKeptTools; drop > 0 {
		ids := slices.SortedFunc(maps.Keys(p.tools), func(a, b string) int { return cmp.Compare(p.tools[a].updated, p.tools[b].updated) })
		for _, id := range ids[:drop] {
			delete(p.tools, id)
			delete(p.plans, id)
		}
	}
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
