package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const memoryUsage = `usage: aether memory <command> [--workspace <name-or-id>] [options]

Put -- before text that starts with a dash. Guide: docs/memory.md

commands:
  ls      [--kind K] [--file P] [--all] [--limit N]        list the newest notes, 100 by default
  search  [--kind K] [--file P] [--all] [--limit N] WORDS  list notes matching any word, best 10 first
  show    ID                                               print a note, its provenance and supersede chain
  add     --kind K [--pin] [--me] [--file P]... [--supersedes ID] TEXT
                                                           add a decision, convention, gotcha or preference
  pin     [--me] ID                                        put a note in every new run's brief; --me, or a member-scope
                                                           note, puts it in yours only
  unpin   ID                                               take a note out of briefs
  rm      ID | --run RUN                                   delete a note, or every note a run wrote
  brief   [--member M]                                     print the brief a new run gets, byte for byte; --member is
                                                           admin only

Every member reads every note. Only a member note's owner or an admin may
pin, unpin, remove or replace it.`

// memoryCall is a parsed memory command, run once connected.
type memoryCall func(c *protocol.Client, w io.Writer) error

func init() {
	register(command{
		name:  "memory",
		short: "search, add, pin and remove the team memory notes runs read",
		usage: "aether memory ls [--workspace <name>] [--kind <kind>] [--file <path>] [--all] [--limit <n>]\n" +
			"aether memory search [--workspace <name>] [--kind <kind>] [--file <path>] [--all] [--limit <n>] <words...>\n" +
			"aether memory show [--workspace <name>] <id>\n" +
			"aether memory add [--workspace <name>] --kind <kind> [--pin] [--me] [--file <path>]... [--supersedes <id>] <text...>\n" +
			"aether memory pin [--workspace <name>] [--me] <id>\n" +
			"aether memory unpin [--workspace <name>] <id>\n" +
			"aether memory rm [--workspace <name>] <id>\n" +
			"aether memory rm [--workspace <name>] --run <run-id>\n" +
			"aether memory brief [--workspace <name>] [--member <member>]",
		example: "aether memory ls\n" +
			"aether memory search --workspace app migrations\n" +
			"aether memory add --kind convention --file go.mod Run go mod tidy before committing\n" +
			"aether memory show <id>\n" +
			"aether memory brief",
		run: runMemory,
	})
}

func runMemory(args []string) error {
	if len(args) == 1 && args[0] == "help" {
		fmt.Println(memoryUsage)
		return nil
	}
	call, err := parseMemory(args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error { return call(c, os.Stdout) })
}

// parseMemory checks a memory command's arguments before anything dials
// the server.
func parseMemory(args []string) (memoryCall, error) {
	if len(args) == 0 {
		return nil, errors.New(memoryUsage)
	}
	name := args[0]
	fs := newFlagSet("memory " + name)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	usage := func(err error) error { return fmt.Errorf("memory %s: %w\n%s", name, err, memoryUsage) }
	switch name {
	case "ls", "search":
		kind := fs.String("kind", "", "only notes of this kind")
		file := fs.String("file", "", "only notes citing this repository path")
		all := fs.Bool("all", false, "include replaced notes")
		most := protocol.MemorySearchLimit
		if name == "ls" {
			most = protocol.MemorySearchMaxLimit
		}
		limit := fs.Int("limit", most, "most notes to list")
		words, err := memoryArgs(fs, args[1:])
		if err != nil {
			return nil, usage(err)
		}
		if name == "ls" && len(words) > 0 {
			return nil, usage(errors.New("ls takes no words; use aether memory search"))
		}
		if name == "search" && len(words) == 0 {
			return nil, usage(errors.New("search needs words; aether memory ls lists the newest notes"))
		}
		return func(c *protocol.Client, w io.Writer) error {
			ws, err := resolveWorkspace(c, *workspace)
			if err != nil {
				return err
			}
			var res protocol.MemorySearchResult
			if err := c.Call(protocol.MethodMemorySearch, protocol.MemorySearchParams{WorkspaceID: ws,
				Words: strings.Join(words, " "), Kind: *kind, File: *file, All: *all, Limit: *limit}, &res); err != nil {
				return err
			}
			if len(res.Items) == 0 {
				_, err := fmt.Fprintln(w, "no notes")
				return err
			}
			return printMemoryList(w, res.Items, time.Now())
		}, nil
	case "show":
		id, err := memoryID(fs, args[1:])
		if err != nil {
			return nil, usage(err)
		}
		return func(c *protocol.Client, w io.Writer) error {
			ws, err := resolveWorkspace(c, *workspace)
			if err != nil {
				return err
			}
			var res protocol.MemoryShowResult
			if err := c.Call(protocol.MethodMemoryShow, protocol.MemoryShowParams{WorkspaceID: ws, ID: id}, &res); err != nil {
				return err
			}
			return printMemoryNote(w, res, time.Now())
		}, nil
	case "add":
		p := protocol.MemoryAddParams{}
		fs.StringVar(&p.Kind, "kind", "", "decision, convention, gotcha or preference")
		fs.BoolVar(&p.Pin, "pin", false, "pin the note into briefs")
		me := fs.Bool("me", false, "a pin on the note enters only your own runs' briefs")
		fs.Var((*stringList)(&p.Files), "file", "repository path the note cites (repeatable)")
		fs.StringVar(&p.Supersedes, "supersedes", "", "ID of the note this one replaces")
		words, err := memoryArgs(fs, args[1:])
		if err != nil {
			return nil, usage(err)
		}
		if p.Kind == "" {
			return nil, usage(errors.New("--kind is required: decision, convention, gotcha or preference"))
		}
		if p.Text = strings.Join(words, " "); strings.TrimSpace(p.Text) == "" {
			return nil, usage(errors.New("the note text is required"))
		}
		if *me {
			p.Scope = protocol.MemoryScopeMember
		}
		return func(c *protocol.Client, w io.Writer) error {
			ws, err := resolveWorkspace(c, *workspace)
			if err != nil {
				return err
			}
			p.WorkspaceID = ws
			var res protocol.MemoryAddResult
			if err := c.Call(protocol.MethodMemoryAdd, p, &res); err != nil {
				return err
			}
			return printMemoryAdded(w, p, res, time.Now())
		}, nil
	case "pin":
		me := fs.Bool("me", false, "pin into your own runs' briefs only (default: the note's own scope)")
		id, err := memoryID(fs, args[1:])
		if err != nil {
			return nil, usage(err)
		}
		p := protocol.MemoryPinParams{ID: id}
		if *me {
			p.Scope = protocol.MemoryScopeMember
		}
		return func(c *protocol.Client, w io.Writer) error {
			ws, err := resolveWorkspace(c, *workspace)
			if err != nil {
				return err
			}
			p.WorkspaceID = ws
			var res protocol.MemoryPinResult
			if err = c.Call(protocol.MethodMemoryPin, p, &res); err != nil {
				return err
			}
			done := "every new run's brief lists it"
			if res.Item.Scope == protocol.MemoryScopeMember {
				done = "your new runs' briefs list it"
			}
			_, err = fmt.Fprintf(w, "pinned %s: %s\n", id, done)
			return err
		}, nil
	case "unpin":
		id, err := memoryID(fs, args[1:])
		if err != nil {
			return nil, usage(err)
		}
		return func(c *protocol.Client, w io.Writer) error {
			ws, err := resolveWorkspace(c, *workspace)
			if err != nil {
				return err
			}
			if err = c.Call(protocol.MethodMemoryUnpin, protocol.MemoryUnpinParams{WorkspaceID: ws, ID: id}, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(w, "unpinned %s: it stays searchable\n", id)
			return err
		}, nil
	case "rm":
		run := fs.String("run", "", "delete every note this run wrote")
		words, err := memoryArgs(fs, args[1:])
		if err != nil {
			return nil, usage(err)
		}
		p := protocol.MemoryRmParams{RunID: *run}
		switch {
		case len(words) == 1 && *run == "":
			p.ID = words[0]
		case len(words) != 0 || *run == "":
			return nil, usage(errors.New("rm takes one note ID, or --run RUN"))
		}
		return func(c *protocol.Client, w io.Writer) error {
			ws, err := resolveWorkspace(c, *workspace)
			if err != nil {
				return err
			}
			p.WorkspaceID = ws
			var res protocol.MemoryRmResult
			if err = c.Call(protocol.MethodMemoryRm, p, &res); err != nil {
				return err
			}
			if len(res.IDs) == 0 {
				_, err = fmt.Fprintf(w, "run %s wrote no notes\n", p.RunID)
				return err
			}
			for _, id := range res.IDs {
				if _, err := fmt.Fprintf(w, "removed %s\n", id); err != nil {
					return err
				}
			}
			return nil
		}, nil
	case "brief":
		member := fs.String("member", "", "whose brief: member ID or display name (default: yours; another member's needs the admin role)")
		if words, err := memoryArgs(fs, args[1:]); err != nil || len(words) > 0 {
			return nil, usage(cmp.Or(err, errors.New("brief takes no arguments")))
		}
		return func(c *protocol.Client, w io.Writer) error {
			ws, err := resolveWorkspace(c, *workspace)
			if err != nil {
				return err
			}
			p := protocol.MemoryBriefParams{WorkspaceID: ws}
			if *member != "" {
				m, merr := resolveMember(c, *member)
				if merr != nil {
					return merr
				}
				p.MemberID = m.ID
			}
			var res protocol.MemoryBriefResult
			if err = c.Call(protocol.MethodMemoryBrief, p, &res); err != nil {
				return err
			}
			if res.Text == "" {
				_, err = fmt.Fprintln(os.Stderr, "no brief: the workspace has no notes and no run finished in the last 48h")
				return err
			}
			if _, err = io.WriteString(w, res.Text); err != nil {
				return err
			}
			// The brief ends without a newline; add one only for a person
			// reading it, so redirected output stays byte-exact.
			if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
				_, err = fmt.Fprintln(w)
			}
			return err
		}, nil
	}
	return nil, fmt.Errorf("unknown memory command %q\n%s", name, memoryUsage)
}

// memoryArgs parses flags placed anywhere among args and returns the other
// words; everything after "--" is a word.
func memoryArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var words []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if n := len(args) - len(rest); len(rest) == 0 || n > 0 && args[n-1] == "--" {
			return append(words, rest...), nil
		}
		words, args = append(words, rest[0]), rest[1:]
	}
}

func memoryID(fs *flag.FlagSet, args []string) (string, error) {
	words, err := memoryArgs(fs, args)
	if err != nil {
		return "", err
	}
	if len(words) != 1 {
		return "", errors.New("expected one note ID")
	}
	return words[0], nil
}

func printMemoryList(w io.Writer, items []protocol.MemoryItem, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tKIND\tSOURCE\tBY\tRUN\tAGE\tPINNED\tTITLE")
	for _, item := range items {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", item.ID, item.Kind, item.Source, memoryBy(item),
			cmp.Or(item.RunID, "-"), memoryAge(item.CreatedAt, now), memoryPinned(item), printable(memoryTitle(item)))
	}
	return tw.Flush()
}

func printMemoryNote(w io.Writer, res protocol.MemoryShowResult, now time.Time) error {
	item := res.Item
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "%s %s from %s, %s scope, %s old\n", item.ID, item.Kind, item.Source, item.Scope, memoryAge(item.CreatedAt, now))
	field := func(name, value string) {
		if value != "" {
			_, _ = fmt.Fprintf(tw, "%s:\t%s\n", name, printable(value))
		}
	}
	author := item.AuthorID
	if item.AuthorName != "" {
		author = item.AuthorName + " (" + item.AuthorID + ")"
	}
	field("Author", author)
	field("Run", item.RunID)
	field("Harness", item.Harness)
	field("Commit", item.Commit)
	field("Files", strings.Join(item.Files, ", "))
	if item.PRNumber > 0 {
		field("Pull request", fmt.Sprintf("#%d %s %s", item.PRNumber, item.PRState, item.PRURL))
	}
	if item.PinnedAt != nil {
		field("Pinned", memoryPinned(item)+", since "+*item.PinnedAt)
	}
	field("Replaced by", item.SupersededBy)
	if item.InvalidAt != nil {
		field("Invalid since", *item.InvalidAt)
	}
	field("Created", item.CreatedAt)
	if err := tw.Flush(); err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(item.Body, "\n"), "\n")
	for i, line := range lines {
		lines[i] = printable(line)
	}
	if _, err := fmt.Fprintf(w, "\n%s\n", strings.Join(lines, "\n")); err != nil {
		return err
	}
	if len(res.Chain) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\nSupersede chain, in replacement order:"); err != nil {
		return err
	}
	return printMemoryList(w, res.Chain, now)
}

func printMemoryAdded(w io.Writer, p protocol.MemoryAddParams, res protocol.MemoryAddResult, now time.Time) error {
	item := res.Item
	line := "added " + item.ID
	switch {
	case res.Duplicate:
		line = item.ID + " already holds this text; nothing was added"
	case p.Supersedes != "":
		line += ", replacing " + p.Supersedes
	}
	switch {
	case item.PinnedAt != nil:
		line += "; pinned, " + memoryPinned(item)
	default:
		line += "; searchable now, in briefs once pinned: aether memory pin " + item.ID
	}
	if _, err := fmt.Fprintln(w, line); err != nil {
		return err
	}
	if len(res.Similar) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\nSimilar notes:"); err != nil {
		return err
	}
	return printMemoryList(w, res.Similar, now)
}

// memoryBy is who wrote the note: the member, and the agent for a run's.
func memoryBy(item protocol.MemoryItem) string {
	by := cmp.Or(item.AuthorName, item.AuthorID, "-")
	if item.Harness != "" {
		by += "/" + item.Harness
	}
	return by
}

func memoryPinned(item protocol.MemoryItem) string {
	switch {
	case item.PinnedAt == nil:
		return "-"
	case item.Scope == protocol.MemoryScopeMember:
		return "for " + cmp.Or(item.PinnedByName, item.PinnedBy) + " only"
	}
	return "for the workspace"
}

func memoryTitle(item protocol.MemoryItem) string {
	switch {
	case item.SupersededBy != "":
		return item.Title + " (replaced by " + item.SupersededBy + ")"
	case item.InvalidAt != nil:
		return item.Title + " (invalid)"
	}
	return item.Title
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
