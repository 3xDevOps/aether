package coordcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func memoryResult(t *testing.T, v any) protocol.Response {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Response{Result: data}
}

func runMemoryCLI(t *testing.T, socket string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code, err := Run(t.Context(), append([]string{"memory"}, args...), Config{Socket: socket, In: strings.NewReader(""), Out: &out, ErrOut: &errOut})
	if err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errOut.String()
}

func ago(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

func TestMemorySearchPrintsCompactLines(t *testing.T) {
	items := []protocol.MemoryItem{{
		ID: "note-ab12cd34ef", Kind: "gotcha", Source: "agent", AuthorName: "dana", Harness: "codex", RunID: "run-x1y2z3",
		Files: []string{"web/a.go", "web/b.go", "web/c.go", "web/d.go", "web/e.go"}, PRNumber: 312, PRState: "merged",
		Body: "Bun lockfile drifts\nwhen npm runs in web/; " + strings.Repeat("x", 120), CreatedAt: ago(3 * time.Hour),
	}, {
		ID: "note-old0000000", Kind: "decision", Source: "member", AuthorID: "mem-alice", SupersededBy: "note-new0000000",
		Body: "Use tabs.", CreatedAt: ago(72 * time.Hour),
	}}
	for i := range 10 {
		items = append(items, protocol.MemoryItem{ID: fmt.Sprintf("note-fill%06d", i), Kind: "outcome", Source: "report", Body: "filler", CreatedAt: ago(time.Minute)})
	}
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return memoryResult(t, protocol.MemorySearchResult{Items: items})
	})
	code, out, errOut := runMemoryCLI(t, s.path, "search", "--kind", "gotcha", "bun", "--file", "web/package.json", "--all", "lockfile")
	if code != ExitOK || errOut != "" {
		t.Fatalf("search = %d, stderr %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if lines[0] != coordhooks.MemoryRule {
		t.Fatalf("search output must open with MemoryRule: %q", out)
	}
	var notes []string
	for _, line := range lines {
		if strings.HasPrefix(line, "note-") {
			notes = append(notes, line)
		}
	}
	if len(notes) != 10 {
		t.Fatalf("search printed %d notes, want at most 10:\n%s", len(notes), out)
	}
	want := "note-ab12cd34ef gotcha agent dana/codex run-x1y2z3 3h files=web/a.go,web/b.go,web/c.go,+2 PR #312 merged: Bun lockfile drifts when npm runs in web/; " + strings.Repeat("x", 57) + "…"
	if notes[0] != want {
		t.Fatalf("compact line\n got %q\nwant %q", notes[0], want)
	}
	if notes[1] != "note-old0000000 decision member mem-alice 3d replaced by note-new0000000: Use tabs." {
		t.Fatalf("replaced note line = %q", notes[1])
	}
	var p protocol.MemorySearchParams
	if err := json.Unmarshal(s.requests()[0].Params, &p); err != nil {
		t.Fatal(err)
	}
	if s.requests()[0].Method != protocol.MethodMemorySearch || p != (protocol.MemorySearchParams{Words: "bun lockfile", Kind: "gotcha", File: "web/package.json", All: true, Limit: protocol.MemorySearchLimit}) {
		t.Fatalf("search request = %s %+v", s.requests()[0].Method, p)
	}
}

func TestMemoryAddSendsTheNoteAndPrintsTheReceipt(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return memoryResult(t, protocol.MemoryAddResult{
			Item:    protocol.MemoryItem{ID: "note-new0000000", Kind: "gotcha", Source: "agent", CreatedAt: ago(0)},
			Similar: []protocol.MemoryItem{{ID: "note-sim0000000", Kind: "gotcha", Source: "agent", AuthorName: "ben", Body: "Close the pool first.", CreatedAt: ago(5 * time.Minute)}},
		})
	})
	code, out, errOut := runMemoryCLI(t, s.path, "add", "--kind", "gotcha", "--file", "a.go", "Close", "the", "pool", "--file", "b.go", "--supersedes", "note-old0000000", "--scope", "me", "--", "--before", "exit")
	if code != ExitOK || errOut != "" {
		t.Fatalf("add = %d, stderr %q", code, errOut)
	}
	var p protocol.MemoryAddParams
	if err := json.Unmarshal(s.requests()[0].Params, &p); err != nil {
		t.Fatal(err)
	}
	if s.requests()[0].Method != protocol.MethodMemoryAdd || p.Kind != "gotcha" || p.Text != "Close the pool --before exit" ||
		!slices.Equal(p.Files, []string{"a.go", "b.go"}) || p.Supersedes != "note-old0000000" || p.Scope != protocol.MemoryScopeMember || p.Pin || p.WorkspaceID != "" {
		t.Fatalf("add request = %s %+v", s.requests()[0].Method, p)
	}
	for _, want := range []string{
		coordhooks.MemoryRule + "\n",
		"note-new0000000 saved, replacing note-old0000000.\n",
		"Searchable now; reaches new runs only after a member pins it: aether memory pin note-new0000000\n",
		"Similar notes:\nnote-sim0000000 gotcha agent ben 5m: Close the pool first.\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("receipt lacks %q:\n%s", want, out)
		}
	}
}

func TestMemoryCLIPrintsServerErrorsVerbatim(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  protocol.Error
		exit int
	}{
		{[]string{"add", "--kind", "gotcha", "token=abc"}, protocol.Error{Code: protocol.CodeDenied, Message: "memory.add: rejected, gitleaks rule generic-api-key at 1:1; remove the secret and retry"}, ExitDenied},
		{[]string{"add", "--kind", "convention", "--supersedes", "note-ab12", "Use bun"}, protocol.Error{Code: protocol.CodeDenied, Message: "memory.add: note-ab12 is pinned by dana; ask a member to replace it (aether memory add --supersedes note-ab12)"}, ExitDenied},
		{[]string{"show", "note-gone000000"}, protocol.Error{Code: protocol.CodeNotFound, Message: "memory.show: note note-gone000000 not found"}, ExitMissing},
		{[]string{"search", "bun"}, protocol.Error{Code: protocol.CodeUnavailable, Message: "memory.search: run has finished"}, ExitMissing},
		{[]string{"brief"}, protocol.Error{Code: protocol.CodeInternal, Message: "memory.brief: database is locked"}, ExitFailure},
	} {
		s := newCLISocket(t, func(protocol.Request) protocol.Response { return protocol.Response{Error: &tc.err} })
		code, out, errOut := runMemoryCLI(t, s.path, tc.args...)
		if code != tc.exit || out != "" || errOut != fmt.Sprintf("%s [aether error %d]\n", tc.err.Message, tc.err.Code) {
			t.Fatalf("%v = %d, stdout %q, stderr %q", tc.args, code, out, errOut)
		}
	}
}

func TestMemoryCLIUsage(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		t.Error("a usage error reached the socket")
		return protocol.Response{}
	})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "memory requires a subcommand"},
		{[]string{"forget"}, "unknown memory command: forget"},
		{[]string{"add", "no kind"}, "--kind is required"},
		{[]string{"add", "--kind", "gotcha"}, "the note text is required"},
		{[]string{"add", "--kind", "gotcha", "--scope", "workspace", "text"}, `--scope takes only "me"`},
		{[]string{"search", "--pinned"}, "flag provided but not defined: -pinned"},
		{[]string{"show"}, "memory show takes one note ID"},
		{[]string{"brief", "extra"}, "memory brief takes no arguments"},
	} {
		code, out, errOut := runMemoryCLI(t, s.path, tc.args...)
		if code != ExitUsage || out != "" || !strings.Contains(errOut, tc.want) || !strings.Contains(errOut, "usage: aether-internal memory") {
			t.Fatalf("%v = %d, stdout %q, stderr %q", tc.args, code, out, errOut)
		}
	}
	for _, command := range []string{"memory", "memory add", "memory search", "memory show", "memory brief"} {
		code, out := runCLI(t, s.path, append(strings.Fields(command), "--help"), "")
		if code != ExitOK || !strings.HasPrefix(out, "usage: aether-internal "+command) {
			t.Fatalf("%s --help = %d %q", command, code, out)
		}
	}
}

func TestMemoryShowPrintsProvenanceAndChain(t *testing.T) {
	pinned := ago(time.Hour)
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return memoryResult(t, protocol.MemoryShowResult{
			Item: protocol.MemoryItem{
				ID: "note-mid0000000", Kind: "decision", Scope: "workspace", Source: "agent", AuthorName: "dana", AuthorID: "mem-dana",
				RunID: "run-a1", Harness: "claude", Commit: "0123abcd", Files: []string{"go.mod"}, PRNumber: 7, PRState: "open",
				PRURL: "https://github.com/example/repo/pull/7", PinnedByName: "ben", PinnedAt: &pinned, SupersededBy: "note-new0000000",
				Body: "Pin the toolchain.\nThe default go is newer.", CreatedAt: ago(2 * time.Hour),
			},
			Chain: []protocol.MemoryItem{
				{ID: "note-old0000000", Kind: "decision", Source: "agent", SupersededBy: "note-mid0000000", Body: "Use any go.", CreatedAt: ago(3 * time.Hour)},
				{ID: "note-new0000000", Kind: "decision", Source: "member", Body: "Use go1.26.9.", CreatedAt: ago(time.Hour)},
			},
		})
	})
	code, out, errOut := runMemoryCLI(t, s.path, "show", "note-mid0000000")
	if code != ExitOK || errOut != "" {
		t.Fatalf("show = %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		coordhooks.MemoryRule + "\nnote-mid0000000 decision, agent note, workspace scope, 2h old\n",
		"Author: dana (mem-dana)\nRun: run-a1\nHarness: claude\nCommit: 0123abcd\nFiles: go.mod\n",
		"Pull request: #7 open https://github.com/example/repo/pull/7\n",
		"Pinned: by ben at " + pinned + "\n",
		"Replaced by: note-new0000000\n",
		"\nPin the toolchain.\nThe default go is newer.\n",
		"Supersede chain, oldest first:\nnote-old0000000 decision agent 3h replaced by note-mid0000000: Use any go.\nnote-mid0000000 decision agent dana/claude run-a1 2h files=go.mod PR #7 open replaced by note-new0000000: Pin the toolchain. The default go is newer. (this note)\nnote-new0000000 decision member 1h: Use go1.26.9.\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show lacks %q:\n%s", want, out)
		}
	}
	var p protocol.MemoryShowParams
	if err := json.Unmarshal(s.requests()[0].Params, &p); err != nil || p.ID != "note-mid0000000" {
		t.Fatalf("show params = %+v, %v", p, err)
	}
}

func TestMemoryBriefPrintsExactBytes(t *testing.T) {
	brief := "<team-memory workspace=\"api\">\n" + coordhooks.MemoryRule + "\nPinned:\n- note-ab12 convention (dana): Use bun in web/; never npm.\n</team-memory>"
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		if len(req.Params) != 0 {
			t.Errorf("memory.brief sent params %s", req.Params)
		}
		return memoryResult(t, protocol.MemoryBriefResult{Text: brief})
	})
	if code, out, errOut := runMemoryCLI(t, s.path, "brief"); code != ExitOK || out != brief || errOut != "" {
		t.Fatalf("brief = %d, %q, stderr %q", code, out, errOut)
	}
}

func TestSkillTeachesTeamMemoryByCapability(t *testing.T) {
	for _, tc := range []struct {
		capabilities []string
		want, refuse []string
	}{
		{nil, nil, []string{"Team memory:"}},
		{[]string{protocol.MethodMemorySearch, protocol.MethodMemoryShow, protocol.MethodMemoryBrief},
			[]string{"Team memory:\n" + coordhooks.MemoryRule, "before debugging a failure or re-deciding a convention", "memory search --file"},
			[]string{"memory add"}},
		{[]string{protocol.MethodMemorySearch, protocol.MethodMemoryAdd},
			[]string{"record a gotcha", "record the decision", "memory add --kind gotcha --file", "AGENTS.md or CLAUDE.md through a pull request"}, nil},
	} {
		s := newCLISocket(t, func(protocol.Request) protocol.Response {
			return memoryResult(t, protocol.CoordStatusResult{RunID: "run-1", Capabilities: append([]string{protocol.MethodCoordStatus}, tc.capabilities...)})
		})
		code, out := runCLI(t, s.path, []string{"skill"}, "")
		if code != ExitOK {
			t.Fatalf("skill = %d %s", code, out)
		}
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("%v: skill lacks %q:\n%s", tc.capabilities, want, out)
			}
		}
		for _, refuse := range tc.refuse {
			if strings.Contains(out, refuse) {
				t.Errorf("%v: skill carries %q:\n%s", tc.capabilities, refuse, out)
			}
		}
	}
}
