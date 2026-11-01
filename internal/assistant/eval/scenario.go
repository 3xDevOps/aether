package eval

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

var (
	urlPattern      = regexp.MustCompile(`https?://\S+|\S*\?[a-z_]+=\S*`)
	codePattern     = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")
	questionPattern = regexp.MustCompile(`[^.!?\n]*\?`)
	offerPattern    = regexp.MustCompile(`(?i)\b(show|see|edit|change|tweak|adjust|review)\b[^?]*\b(brief|task|prompt)\b|\b(brief|task|prompt)\b[^?]*\b(show|edit|change|tweak|adjust)`)
	negationBefore  = regexp.MustCompile(`(?i)\b(no|not|never|without|don't|dont|do not|doesn't|avoid|skip)\b(\s+[\w'()-]+){0,8}\s*$`)
)

func wordPattern(w string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(w) + `s?([^\p{L}\p{N}]|$)`)
}

//go:embed testdata/scenarios.json
var scenariosJSON []byte

// Scenario is one synthetic situation: what the member says, what the fake
// control surface answers, and what the agent must and must not do.
type Scenario struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	// History is the earlier conversation a reopened session carries.
	History string `json:"history,omitempty"`
	// ViewContext is the JSON a dashboard prompt carries; empty for a prompt
	// from another device.
	ViewContext json.RawMessage `json:"view_context,omitempty"`
	Member      struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"member"`
	// Answers are the fake control surface's results by RPC method, or by
	// "method:id" for one run; the scenario's own override the shared
	// base.
	Answers map[string]json.RawMessage `json:"answers"`
	// Stubs are tools the registry does not offer yet, modeled from the
	// integrator's contract. A scenario that needs one is "pending
	// capability" until the registry offers it.
	Stubs map[string]Stub `json:"stubs,omitempty"`
	Check Checks          `json:"checks"`
	// Review lists what a person should read in the output; the keyword checks
	// cannot judge it.
	Review []string `json:"review,omitempty"`
}

type Stub struct {
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

// Checks are a scenario's deterministic expectations. Text matches are
// case-insensitive whole words or phrases, with an optional plural "s"; each
// group in a list is satisfied by any one of its alternatives.
type Checks struct {
	// Launches is the number of aether_launch_session
	// calls expected; nil does not check it.
	Launches *int `json:"launches,omitempty"`
	// Saves is the number of successful calls to SaveTool.
	Saves    *int   `json:"saves,omitempty"`
	SaveTool string `json:"save_tool,omitempty"`
	// Params are the fields the launch call must carry.
	Params map[string]string `json:"params,omitempty"`
	// BriefAll and BriefNone apply to the task or objective of every launch.
	BriefAll [][]string `json:"brief_all,omitempty"`
	// MustKeep are the member's prohibitions and limits; a launch that drops
	// one is counted as a dropped must-not.
	MustKeep  [][]string `json:"must_keep,omitempty"`
	BriefNone []string   `json:"brief_none,omitempty"`
	// Invented are words that would be scope the member never asked for.
	Invented []string `json:"invented,omitempty"`
	// AnswerAll and AnswerNone apply to the agent's final reply.
	AnswerAll  [][]string `json:"answer_all,omitempty"`
	AnswerNone []string   `json:"answer_none,omitempty"`
	// NoQuestions fails a reply that asks the member something.
	NoQuestions bool `json:"no_questions,omitempty"`
	// Forbidden are tools the agent must not call.
	Forbidden []string `json:"forbidden,omitempty"`
}

// Load returns the embedded scenarios.
func Load() ([]Scenario, error) {
	var f struct {
		Base      map[string]json.RawMessage `json:"base"`
		Scenarios []Scenario                 `json:"scenarios"`
	}
	if err := json.Unmarshal(scenariosJSON, &f); err != nil {
		return nil, fmt.Errorf("eval: scenarios: %w", err)
	}
	for i := range f.Scenarios {
		merged := maps.Clone(f.Base)
		maps.Copy(merged, f.Scenarios[i].Answers)
		f.Scenarios[i].Answers = merged
	}
	return f.Scenarios, nil
}

// Pending lists the stubbed tools the registry does not offer yet.
func (s Scenario) Pending() []string {
	var out []string
	for name := range s.Stubs {
		if !Offered(name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// Outcome is what one agent run produced.
type Outcome struct {
	Calls  []ToolCall
	Answer string
}

// CheckResult is one check's verdict.
type CheckResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// Flags are the counted failure modes.
type Flags struct {
	UnnecessaryQuestions int `json:"unnecessary_questions"`
	// OfferedFollowUps are questions after a completed launch that offer to
	// show or edit the brief; they are reported, not counted as unnecessary.
	OfferedFollowUps int `json:"offered_follow_ups"`
	UnwantedLaunches int `json:"unwanted_launches"`
	MustNotDropped   int `json:"must_not_dropped"`
	InventedScope    int `json:"invented_scope"`
}

type launch struct {
	tool string
	args map[string]any
}

func (l launch) brief() string {
	for _, k := range []string{"task", "objective"} {
		if v, ok := l.args[k].(string); ok {
			return v
		}
	}
	return ""
}

// launches are the launch calls in calls; a rejected call counts only when
// rejected is true.
func launches(calls []ToolCall, rejected bool) []launch {
	var out []launch
	for _, c := range calls {
		if c.Tool != "aether_launch_session" || c.Error && !rejected {
			continue
		}
		var a map[string]any
		_ = json.Unmarshal(c.Args, &a)
		out = append(out, launch{c.Tool, a})
	}
	return out
}

func has(text string, group []string) bool {
	return slices.ContainsFunc(group, func(alt string) bool { return wordPattern(alt).MatchString(text) })
}

// affirms reports whether text uses word other than to rule it out, as in
// "no refactors" or "do not merge".
func affirms(text, word string) bool {
	for _, m := range wordPattern(word).FindAllStringIndex(text, -1) {
		before := text[:m[0]]
		if i := strings.LastIndexAny(before, ".,;:!?\n"); i >= 0 {
			before = before[i+1:]
		}
		if !negationBefore.MatchString(before) {
			return true
		}
	}
	return false
}

// Score checks an outcome against the scenario's expectations.
func Score(s Scenario, o Outcome) ([]CheckResult, Flags) {
	var res []CheckResult
	var fl Flags
	add := func(name string, pass bool, detail string) {
		res = append(res, CheckResult{Name: name, Pass: pass, Detail: detail})
	}
	c := s.Check
	ls := launches(o.Calls, false)
	if c.Launches != nil {
		add("launch count", len(ls) == *c.Launches, fmt.Sprintf("want %d, got %d", *c.Launches, len(ls)))
		fl.UnwantedLaunches = max(0, len(launches(o.Calls, true))-*c.Launches)
	}
	if c.Saves != nil {
		n := 0
		for _, call := range o.Calls {
			if call.Tool == c.SaveTool && !call.Error {
				n++
			}
		}
		add("saved defaults", n == *c.Saves, fmt.Sprintf("want %d, got %d", *c.Saves, n))
	}
	for _, k := range sortedKeys(c.Params) {
		ok := len(ls) > 0
		var got []string
		for _, l := range ls {
			v, _ := l.args[k].(string)
			got = append(got, v)
			ok = ok && strings.EqualFold(v, c.Params[k])
		}
		add("launch "+k, ok, fmt.Sprintf("want %q, got %q", c.Params[k], got))
	}
	for _, g := range c.BriefAll {
		ok := len(ls) > 0
		for _, l := range ls {
			ok = ok && has(l.brief(), g)
		}
		add("brief keeps "+strings.Join(g, "|"), ok, "")
	}
	for _, g := range c.MustKeep {
		ok := len(ls) > 0
		for _, l := range ls {
			ok = ok && has(l.brief(), g)
		}
		if !ok && len(ls) > 0 {
			fl.MustNotDropped++
		}
		add("brief keeps must-not "+strings.Join(g, "|"), ok, "")
	}
	for _, w := range c.BriefNone {
		bad := slices.ContainsFunc(ls, func(l launch) bool { return affirms(l.brief(), w) })
		add("brief omits "+w, !bad, "")
	}
	for _, w := range c.Invented {
		bad := slices.ContainsFunc(ls, func(l launch) bool { return affirms(l.brief(), w) })
		if bad {
			fl.InventedScope++
		}
		add("no invented "+w, !bad, "")
	}
	for _, g := range c.AnswerAll {
		add("answer says "+strings.Join(g, "|"), has(o.Answer, g), "")
	}
	for _, w := range c.AnswerNone {
		add("answer omits "+w, !has(o.Answer, []string{w}), "")
	}
	if c.NoQuestions {
		text := urlPattern.ReplaceAllString(codePattern.ReplaceAllString(o.Answer, ""), "")
		for _, q := range questionPattern.FindAllString(text, -1) {
			if len(ls) > 0 && offerPattern.MatchString(q) {
				fl.OfferedFollowUps++
			} else {
				fl.UnnecessaryQuestions++
			}
		}
		add("no question asked", fl.UnnecessaryQuestions == 0, fmt.Sprintf("%d questions", fl.UnnecessaryQuestions))
	}
	for _, t := range c.Forbidden {
		used := slices.ContainsFunc(o.Calls, func(call ToolCall) bool { return call.Tool == t })
		add("did not call "+t, !used, "")
	}
	return res, fl
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
