package ptyhost

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ActivityState is the semantic state most recently asserted by the agent's
// terminal title. Unknown means that no recognized title has been observed.
type ActivityState uint8

const (
	ActivityUnknown ActivityState = iota
	ActivityWorking
	ActivityIdle
	ActivityBlocked
)

// Activity is a point-in-time semantic snapshot of one PTY session.
type Activity struct {
	State      ActivityState
	ObservedAt time.Time
	Stale      bool
}

// staleWorkingTitleTimeout is intentionally driven by AgentActivity snapshots,
// rather than one timer per output frame. A title-less agent output arms this
// fallback; silence by itself never does.
var staleWorkingTitleTimeout = 3 * time.Second

// This classifier is a Go port of Orca's agent-title-core.ts,
// agent-title-status.ts, pi-state-title-marker.ts,
// pi-compatible-synthetic-title.ts, opencode-terminal-title.ts, and
// agent-name-token-match.ts. It is distributed under the MIT License:
// Copyright (c) 2026 Lovecast Inc.
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions: the above copyright
// notice and this permission notice shall be included in all copies or
// substantial portions of the Software. THE SOFTWARE IS PROVIDED "AS IS",
// WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED
// TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
// NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE
// FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
// TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR
// THE USE OR OTHER DEALINGS IN THE SOFTWARE.

var claudeManagementTitleRE = regexp.MustCompile(`(?i)^\s*(?:"(?:.*[\\/])?claude(?:\.(?:exe|cmd|bat|ps1))?"|'(?:.*[\\/])?claude(?:\.(?:exe|cmd|bat|ps1))?'|(?:.*[\\/])?claude(?:\.(?:exe|cmd|bat|ps1))?)\s+agents\s*$`)
var piStateTitleRE = regexp.MustCompile(`(?:^|[\s|])π[ \t]+([:!>])(?:[\s]|$)`)

var legacyAgentNames = [...]string{
	"claude",
	"openclaude",
	"codex",
	"copilot",
	"cursor",
	"gemini",
	"antigravity",
	"opencode",
	"mimo",
	"openclaw",
	"aider",
	"grok",
	"devin",
}

func classifyTitle(title string) (ActivityState, bool) {
	if title == "" || claudeManagementTitleRE.MatchString(title) {
		return ActivityUnknown, false
	}
	if strings.EqualFold(strings.TrimSpace(title), "cursor agent") {
		return ActivityUnknown, false
	}

	if isOpenCodeNativeTitle(title) {
		if containsAgentSpinner(title) {
			return ActivityWorking, true
		}
		return ActivityIdle, true
	}

	// Pi/OMP markers are an explicit state protocol. Their opaque label may
	// contain other agents' glyphs or marker-shaped text.
	if status, ok := piStateTitleStatus(title); ok {
		return status, true
	}
	if strings.ContainsRune(title, '✋') {
		return ActivityBlocked, true
	}
	if strings.ContainsRune(title, '✦') || strings.ContainsRune(title, '⏲') {
		return ActivityWorking, true
	}
	if strings.ContainsRune(title, '◇') {
		return ActivityIdle, true
	}
	if status, ok := piCompatibleSyntheticStatus(title); ok {
		return status, true
	}
	if strings.HasPrefix(title, "✳ ") || title == "✳" {
		return ActivityIdle, true
	}
	if status, ok := piCompatibleSeparatorStatus(title); ok {
		return status, true
	}
	if isLegacyPiTitle(title) && !containsBraille(title) {
		return ActivityIdle, true
	}
	if containsAgentSpinner(title) {
		return ActivityWorking, true
	}

	lower := strings.ToLower(title)
	hasDroid := hasAgentNameLower(lower, "droid")
	hasHermes := hasAgentNameLower(lower, "hermes")
	hasAgy := hasAgentNameLower(lower, "agy")
	hasLegacy := hasLegacyAgentNameLower(lower)
	if !hasDroid && !hasHermes && !hasAgy && !hasLegacy {
		return ActivityUnknown, false
	}
	if containsAnyLower(lower, "action required", "permission", "waiting") {
		return ActivityBlocked, true
	}
	if strongKeywordLower(lower, "ready", "idle", "done") {
		return ActivityIdle, true
	}
	if strongKeywordLower(lower, "working", "thinking", "running") {
		return ActivityWorking, true
	}
	if strings.HasPrefix(title, ". ") {
		return ActivityWorking, true
	}
	if strings.HasPrefix(title, "* ") {
		return ActivityIdle, true
	}
	// Droid's native name-only title is not completion evidence; its hook
	// lifecycle owns that state.
	if hasDroid && !hasLegacy {
		return ActivityUnknown, false
	}
	return ActivityIdle, true
}

func piStateTitleStatus(title string) (ActivityState, bool) {
	match := piStateTitleRE.FindStringSubmatch(title)
	if len(match) != 2 {
		return ActivityUnknown, false
	}
	switch match[1] {
	case ":":
		return ActivityWorking, true
	case "!":
		return ActivityBlocked, true
	case ">":
		return ActivityIdle, true
	default:
		return ActivityUnknown, false
	}
}

func isLegacyPiTitle(title string) bool {
	t := strings.TrimSpace(title)
	if r, size := utf8.DecodeRuneInString(t); r >= 0x2800 && r <= 0x28ff {
		t = strings.TrimSpace(t[size:])
	}
	if !strings.HasPrefix(t, "π") {
		return false
	}
	rest := t[len("π"):]
	if rest == "" {
		return false
	}
	return rest[0] == '-' || rest[0] == ':' || unicode.IsSpace(rune(rest[0]))
}

func piCompatibleSyntheticStatus(title string) (ActivityState, bool) {
	t := strings.TrimSpace(title)
	if r, size := utf8.DecodeRuneInString(t); r >= 0x2800 && r <= 0x28ff {
		t = strings.TrimSpace(t[size:])
	}
	lower := strings.ToLower(t)
	base := ""
	for _, brand := range []string{"pi", "omp"} {
		if lower == brand || strings.HasPrefix(lower, brand+" ") {
			base = brand
			break
		}
	}
	if base == "" {
		return ActivityUnknown, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(lower, base))
	if rest != "" &&
		rest != "ready" &&
		rest != "idle" &&
		rest != "done" &&
		rest != "- action required" {
		return ActivityUnknown, false
	}
	// An animated frame outranks the synthetic permission tail.
	if containsBraille(title) {
		return ActivityWorking, true
	}
	if rest == "- action required" {
		return ActivityBlocked, true
	}
	return ActivityIdle, true
}

func piCompatibleSeparatorStatus(title string) (ActivityState, bool) {
	if containsBraille(title) {
		return ActivityUnknown, false
	}
	t := strings.TrimSpace(title)
	brand := ""
	for _, candidate := range []string{"π", "Pi", "OMP"} {
		if strings.HasPrefix(t, candidate) {
			brand = candidate
			break
		}
	}
	if brand == "" {
		return ActivityUnknown, false
	}
	rest := t[len(brand):]
	if strings.HasPrefix(rest, ":") {
		if len(rest) == 1 || unicode.IsSpace(rune(rest[1])) {
			return ActivityIdle, true
		}
		return ActivityUnknown, false
	}
	if rest == "" || !unicode.IsSpace(rune(rest[0])) {
		return ActivityUnknown, false
	}
	for len(rest) > 0 && unicode.IsSpace(rune(rest[0])) {
		rest = rest[1:]
	}
	if rest == "" || (rest[0] != '!' && rest[0] != '>' && rest[0] != '-') {
		return ActivityUnknown, false
	}
	marker := rest[0]
	if len(rest) > 1 && !unicode.IsSpace(rune(rest[1])) {
		return ActivityUnknown, false
	}
	if strings.Contains(strings.ToLower(t), "action required") {
		return ActivityBlocked, true
	}
	if marker == '!' {
		return ActivityBlocked, true
	}
	return ActivityIdle, true
}
func isOpenCodeNativeTitle(title string) bool {
	t := strings.TrimSpace(title)
	if openCodeMarkerBody(t) {
		return true
	}
	separator := strings.Index(t, " | ")
	if separator < 0 {
		return false
	}
	prefix := strings.TrimSpace(t[:separator])
	if startsDecorative(prefix) {
		return false
	}
	return openCodeMarkerBody(strings.TrimSpace(t[separator+3:]))
}

func openCodeMarkerBody(title string) bool {
	title = strings.TrimSpace(title)
	if r, size := utf8.DecodeRuneInString(title); r == '▣' || (r >= 0x2800 && r <= 0x28ff) {
		if len(title) > size && title[size] == ' ' {
			title = strings.TrimSpace(title[size:])
		}
	}
	if !strings.HasPrefix(title, "OC |") {
		return false
	}
	end := len("OC |")
	for end < len(title) && (title[end] == ' ' || title[end] == '\t') {
		end++
	}
	return end < len(title) && !unicode.IsSpace(rune(title[end]))
}

func startsDecorative(title string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(title)
	return r == '▣' || (r >= 0x2800 && r <= 0x28ff)
}

func containsAgentSpinner(title string) bool {
	return containsBraille(title) || strings.ContainsAny(title, "◐◑◒◓")
}

func containsBraille(title string) bool {
	for _, r := range title {
		if r >= 0x2800 && r <= 0x28ff {
			return true
		}
	}
	return false
}

func hasLegacyAgentNameLower(lower string) bool {
	for _, name := range legacyAgentNames {
		if hasAgentNameLower(lower, name) {
			return true
		}
	}
	return false
}

func hasAgentNameLower(lower, name string) bool {
	for offset := 0; offset < len(lower); {
		rel := strings.Index(lower[offset:], name)
		if rel < 0 {
			return false
		}
		start := offset + rel
		end := start + len(name)
		if !titleNameBoundary(lower, start-1) {
			offset = end
			continue
		}
		if end < len(lower) {
			for _, suffix := range []string{".exe", ".cmd", ".bat", ".ps1"} {
				if strings.HasPrefix(lower[end:], suffix) {
					end += len(suffix)
					break
				}
			}
		}
		if titleNameBoundary(lower, end) {
			return true
		}
		offset = start + len(name)
	}
	return false
}

func titleNameBoundary(title string, index int) bool {
	if index < 0 || index >= len(title) {
		return true
	}
	c := title[index]
	return (c < 'a' || c > 'z') && (c < '0' || c > '9') &&
		(c < 'A' || c > 'Z') && c != '_' && c != '.' && c != '/' && c != '\\' && c != '-'
}

func containsAnyLower(lower string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func strongKeywordLower(lower string, words ...string) bool {
	for _, word := range words {
		for offset := 0; offset < len(lower); {
			rel := strings.Index(lower[offset:], word)
			if rel < 0 {
				break
			}
			start := offset + rel
			end := start + len(word)
			leftOK := start == 0 || !titleKeywordBoundary(lower[start-1], true)
			rightOK := end == len(lower) || !titleKeywordBoundary(lower[end], false)
			if leftOK && rightOK {
				return true
			}
			offset = end
		}
	}
	return false
}

func titleKeywordBoundary(c byte, left bool) bool {
	if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
		return true
	}
	if left && (c == '.' || c == '/' || c == '\\') {
		return true
	}
	return false
}
func (s *session) agentActivity(now time.Time) (Activity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return Activity{}, false
	}
	if s.activity.State == ActivityWorking &&
		!s.staleSince.IsZero() &&
		now.Sub(s.staleSince) >= staleWorkingTitleTimeout {
		// This is deliberately uncertain: process exit is decided by the
		// lifecycle path, never inferred from a title or a quiet PTY.
		s.activity = Activity{
			State:      ActivityIdle,
			ObservedAt: s.staleSince,
			Stale:      true,
		}
		s.staleSince = time.Time{}
	}
	return s.activity, true
}
