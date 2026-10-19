package coordcli

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

var memoryUsages = map[string]string{
	"memory": `usage: aether-internal memory <add|search|show|brief> [options]

Team memory holds short, attributed notes that every run in this workspace
can search and write. ` + coordhooks.MemoryRule + `

Commands:
  add     record a decision, convention, gotcha or preference
  search  list up to 10 matching notes, best first
  show    print one note with its provenance and supersede chain
  brief   print this run's team memory brief

Output is text. A refusal prints the server's error on stderr with a
nonzero exit, as for the other commands.
`,
	"memory add": `usage: aether-internal memory add --kind <decision|convention|gotcha|preference> [--file <path>]... [--supersedes <id>] [--scope me] <text>

Record one note of at most 1024 bytes; its first line is its title. --file
cites a repository path (repeatable, at most 20). --supersedes replaces a
note; an agent may replace only an unpinned note an agent wrote. --scope me
keeps a pin on the note out of other members' briefs. The server records
your member, run, harness and commit, and refuses text holding a secret.
The note is searchable at once and reaches new runs only after a member
pins it. Put -- before text that starts with a dash.
`,
	"memory search": `usage: aether-internal memory search [--kind <kind>] [--file <path>] [--all] [<words>]

Print up to 10 notes matching any of the words, best first; without words,
the newest. Each line: id, kind, source, member/harness, run, age, cited
files, pull request and the first 100 characters. --file keeps notes citing
that path; --all includes replaced notes.
`,
	"memory show": `usage: aether-internal memory show <id>

Print one note's full text, author, run, harness, commit, cited files, pull
request and pin, then the notes it replaced or was replaced by.
`,
	"memory brief": `usage: aether-internal memory brief

Print this run's team memory brief exactly as the server builds it now: pinned
notes and recently finished runs. Prints nothing when there is nothing to
brief.
`,
}

const memorySkill = `Team memory:
` + coordhooks.MemoryRule + `
Search it before debugging a failure or re-deciding a convention:
  aether-internal memory search <words>
  aether-internal memory search --file <path>
`

const memoryAddSkill = `After solving a non-obvious failure, record a gotcha; when choosing between
approaches, record the decision and why:
  aether-internal memory add --kind gotcha --file <path> "<symptom, cause, fix>"
  aether-internal memory add --kind decision "<choice and why>"
A note reaches new runs only after a member pins it. Promote a stable
convention into AGENTS.md or CLAUDE.md through a pull request.
`

func writeMemorySkill(out io.Writer, status *protocol.CoordStatusResult) error {
	if !hasSkillCapability(status, protocol.MethodMemorySearch) {
		return nil
	}
	text := memorySkill
	if hasSkillCapability(status, protocol.MethodMemoryAdd) {
		text += memoryAddSkill
	}
	_, err := io.WriteString(out, text+"  aether-internal memory --help\n")
	return err
}

func memoryCommand(ctx context.Context, cfg Config, args []string) (int, error) {
	command := "memory"
	var text string
	var err error
	if len(args) == 0 {
		err = usageError("memory requires a subcommand: add, search, show or brief")
	} else {
		command += " " + args[0]
		switch args[0] {
		case "add":
			text, err = memoryAdd(ctx, cfg.Socket, args[1:])
		case "search":
			text, err = memorySearch(ctx, cfg.Socket, args[1:])
		case "show":
			text, err = memoryShow(ctx, cfg.Socket, args[1:])
		case "brief":
			text, err = memoryBrief(ctx, cfg.Socket, args[1:])
		default:
			command, err = "memory", usageError("unknown memory command: "+args[0])
		}
	}
	if err != nil {
		message := err.Error() + "\n"
		if _, usage := err.(*CLIUsageError); usage {
			message += memoryUsages[command]
		}
		if _, werr := io.WriteString(cfg.ErrOut, message); werr != nil {
			return ExitFailure, fmt.Errorf("write %s error %q: %w", command, err.Error(), werr)
		}
		return exitFor(errorCode(err)), nil
	}
	if _, err := io.WriteString(cfg.Out, text); err != nil {
		return ExitFailure, fmt.Errorf("write %s output: %w", command, err)
	}
	return ExitOK, nil
}

// memoryArgs parses flags placed anywhere among args and returns the other
// words; everything after "--" is a word.
func memoryArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var words []string
	for {
		if err := parseFlags(fs, args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if n := len(args) - len(rest); len(rest) == 0 || n > 0 && args[n-1] == "--" {
			return append(words, rest...), nil
		}
		words, args = append(words, rest[0]), rest[1:]
	}
}

func memoryAdd(ctx context.Context, socket string, args []string) (string, error) {
	fs := newFlags("memory add")
	kind := fs.String("kind", "", "decision, convention, gotcha or preference")
	var files []string
	fs.Func("file", "cited repository path (repeatable)", func(path string) error {
		files = append(files, path)
		return nil
	})
	supersedes := fs.String("supersedes", "", "ID of the note this one replaces")
	scope := fs.String("scope", "", `"me" keeps a pin on the note out of other members' briefs`)
	words, err := memoryArgs(fs, args)
	if err != nil {
		return "", err
	}
	p := protocol.MemoryAddParams{Kind: *kind, Text: strings.Join(words, " "), Files: files, Supersedes: *supersedes}
	switch *scope {
	case "":
	case "me":
		p.Scope = protocol.MemoryScopeMember
	default:
		return "", usageError(`memory add: --scope takes only "me"`)
	}
	if p.Kind == "" {
		return "", usageError("memory add: --kind is required: decision, convention, gotcha or preference")
	}
	if strings.TrimSpace(p.Text) == "" {
		return "", usageError("memory add: the note text is required")
	}
	var out protocol.MemoryAddResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMemoryAdd, p, &out); err != nil {
		return "", err
	}
	now := time.Now()
	item := out.Item
	var b strings.Builder
	b.WriteString(coordhooks.MemoryRule + "\n")
	switch {
	case out.Duplicate:
		fmt.Fprintf(&b, "%s already holds this text; nothing was added.\n", item.ID)
	case p.Supersedes != "":
		fmt.Fprintf(&b, "%s saved, replacing %s.\n", item.ID, p.Supersedes)
	default:
		fmt.Fprintf(&b, "%s saved.\n", item.ID)
	}
	if item.PinnedAt != nil {
		fmt.Fprintf(&b, "Pinned by %s: it reaches new runs.\n", cmp.Or(item.PinnedByName, item.PinnedBy))
	} else {
		fmt.Fprintf(&b, "Searchable now; reaches new runs only after a member pins it: aether memory pin %s\n", item.ID)
	}
	if len(out.Similar) > 0 {
		b.WriteString("Similar notes:\n")
		for _, similar := range out.Similar {
			b.WriteString(memoryLine(similar, now) + "\n")
		}
	}
	return b.String(), nil
}

func memorySearch(ctx context.Context, socket string, args []string) (string, error) {
	fs := newFlags("memory search")
	kind := fs.String("kind", "", "only notes of this kind")
	file := fs.String("file", "", "only notes citing this repository path")
	all := fs.Bool("all", false, "include replaced notes")
	words, err := memoryArgs(fs, args)
	if err != nil {
		return "", err
	}
	p := protocol.MemorySearchParams{Words: strings.Join(words, " "), Kind: *kind, File: *file, All: *all, Limit: protocol.MemorySearchLimit}
	var out protocol.MemorySearchResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMemorySearch, p, &out); err != nil {
		return "", err
	}
	if len(out.Items) == 0 {
		return "No team memory notes match.\n", nil
	}
	now := time.Now()
	var b strings.Builder
	b.WriteString(coordhooks.MemoryRule + "\n")
	for _, item := range out.Items[:min(len(out.Items), protocol.MemorySearchLimit)] {
		b.WriteString(memoryLine(item, now) + "\n")
	}
	b.WriteString("Full note: aether-internal memory show <id>\n")
	return b.String(), nil
}

func memoryShow(ctx context.Context, socket string, args []string) (string, error) {
	fs := newFlags("memory show")
	if err := parseFlags(fs, args); err != nil {
		return "", err
	}
	if fs.NArg() != 1 {
		return "", usageError("memory show takes one note ID")
	}
	var out protocol.MemoryShowResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMemoryShow, protocol.MemoryShowParams{ID: fs.Arg(0)}, &out); err != nil {
		return "", err
	}
	now := time.Now()
	item := out.Item
	var b strings.Builder
	b.WriteString(coordhooks.MemoryRule + "\n")
	fmt.Fprintf(&b, "%s %s, %s note, %s scope, %s old\n", item.ID, item.Kind, item.Source, item.Scope, memoryAge(item.CreatedAt, now))
	field := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintf(&b, "%s: %s\n", name, strings.TrimSpace(value))
		}
	}
	field("Author", memoryAuthor(item))
	field("Run", item.RunID)
	field("Harness", item.Harness)
	field("Commit", item.Commit)
	field("Files", strings.Join(item.Files, ", "))
	if item.PRNumber > 0 {
		field("Pull request", fmt.Sprintf("#%d %s %s", item.PRNumber, item.PRState, item.PRURL))
	}
	if item.PinnedAt != nil {
		field("Pinned", "by "+cmp.Or(item.PinnedByName, item.PinnedBy)+" at "+*item.PinnedAt)
	}
	field("Replaced by", item.SupersededBy)
	if item.InvalidAt != nil {
		field("Invalid since", *item.InvalidAt)
	}
	field("Created", item.CreatedAt)
	b.WriteString("\n" + strings.TrimRight(item.Body, "\n") + "\n")
	if len(out.Chain) > 0 {
		at := 0
		for i, older := range out.Chain {
			if older.SupersededBy == item.ID {
				at = i + 1
			}
		}
		b.WriteString("\nSupersede chain, oldest first:\n")
		for _, other := range out.Chain[:at] {
			b.WriteString(memoryLine(other, now) + "\n")
		}
		b.WriteString(memoryLine(item, now) + " (this note)\n")
		for _, other := range out.Chain[at:] {
			b.WriteString(memoryLine(other, now) + "\n")
		}
	}
	return b.String(), nil
}

func memoryBrief(ctx context.Context, socket string, args []string) (string, error) {
	fs := newFlags("memory brief")
	if err := parseFlags(fs, args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", usageError("memory brief takes no arguments")
	}
	var out protocol.MemoryBriefResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMemoryBrief, nil, &out); err != nil {
		return "", err
	}
	return out.Text, nil
}

// sessionMemoryBrief is the brief a SessionStart hook appends to the role
// card, or the error that kept it out, so a failed brief never drops the card.
func sessionMemoryBrief(ctx context.Context, socket string, status protocol.CoordStatusResult) string {
	if !slices.Contains(status.Capabilities, protocol.MethodMemoryBrief) {
		return ""
	}
	var out protocol.MemoryBriefResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodMemoryBrief, nil, &out); err != nil {
		return "Team memory brief unavailable: " + err.Error()
	}
	return strings.TrimSuffix(out.Text, "\n")
}

// memoryLine is one note on one line: id, kind, source, member/harness, run,
// age, cited files, pull request and the start of its text.
func memoryLine(item protocol.MemoryItem, now time.Time) string {
	parts := []string{item.ID, item.Kind, item.Source}
	who := cmp.Or(item.AuthorName, item.AuthorID)
	if item.Harness != "" {
		who = strings.TrimPrefix(who+"/"+item.Harness, "/")
	}
	if who != "" {
		parts = append(parts, who)
	}
	if item.RunID != "" {
		parts = append(parts, item.RunID)
	}
	parts = append(parts, memoryAge(item.CreatedAt, now))
	if n := len(item.Files); n > 3 {
		parts = append(parts, fmt.Sprintf("files=%s,+%d", strings.Join(item.Files[:3], ","), n-3))
	} else if n > 0 {
		parts = append(parts, "files="+strings.Join(item.Files, ","))
	}
	if item.PRNumber > 0 {
		parts = append(parts, strings.TrimSpace(fmt.Sprintf("PR #%d %s", item.PRNumber, item.PRState)))
	}
	switch {
	case item.SupersededBy != "":
		parts = append(parts, "replaced by "+item.SupersededBy)
	case item.InvalidAt != nil:
		parts = append(parts, "invalid")
	}
	text := []rune(strings.Join(strings.Fields(item.Body), " "))
	if len(text) > 100 {
		text = append(text[:100], '…')
	}
	return strings.Join(parts, " ") + ": " + string(text)
}

func memoryAuthor(item protocol.MemoryItem) string {
	if item.AuthorName != "" && item.AuthorID != "" {
		return item.AuthorName + " (" + item.AuthorID + ")"
	}
	return cmp.Or(item.AuthorName, item.AuthorID)
}

func memoryAge(created string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return created
	}
	switch d := max(now.Sub(t), 0); {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
