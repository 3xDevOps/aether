package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

func init() {
	register(command{
		name:  "answer",
		short: "answer a session's question or permission request (aether sessions <session-id> lists them)",
		usage: "aether answer <session-id> <request-id> <option-id> [--values <json>]\n" +
			"aether answer <session-id> <question-id> <answer|->",
		example: "aether answer run-d6ay38tj8j req-7 allow-1\n" +
			"aether answer run-d6ay38tj8j q-1 \"use the staging database\"",
		run: runAnswer,
	})
}

const answerUsage = "usage: aether answer <session-id> <request-id> <option-id> [--values '<json object>']\n" +
	"   or: aether answer <session-id> <question-id> \"<answer>\"|-   (a teammate's question)"

func runAnswer(args []string) error {
	if len(args) < 3 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		return errors.New(answerUsage)
	}
	fs := newFlagSet("answer")
	values := fs.String("values", "", "form values for an agent's question that has a form, as a JSON object")
	if err := fs.Parse(args[3:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%s\n   (quote a text answer as one argument)", answerUsage)
	}
	var form map[string]any
	if *values != "" {
		if err := json.Unmarshal([]byte(*values), &form); err != nil {
			return fmt.Errorf("--values must be a JSON object: %w", err)
		}
	}
	return withControl(func(c *protocol.Client) error {
		return answer(c, os.Stdout, os.Stdin, args[0], args[1], args[2], form)
	})
}

func answerID(it protocol.AttentionItem) string {
	switch {
	case it.Request != nil:
		return it.Request.ID
	case it.Message != nil:
		return it.Message.ID
	}
	return ""
}

// An always option would change the agent's policy for later requests
// nobody confirmed.
func answerOptions(it protocol.AttentionItem) []protocol.AttentionOption {
	if it.Request == nil {
		return nil
	}
	if it.Kind != protocol.AttentionKindPermission {
		return it.Request.Options
	}
	var once []protocol.AttentionOption
	for _, o := range it.Request.Options {
		if o.Kind == "allow_once" || o.Kind == "reject_once" {
			once = append(once, o)
		}
	}
	return once
}

// needLines is the CLI's form of the MCP connector's answer_with.
func needLines(it protocol.AttentionItem) []string {
	run, id := it.RunID, answerID(it)
	text := it.Reason
	switch {
	case it.Request != nil:
		text = cmp.Or(it.Request.Title, it.Reason)
	case it.Message != nil:
		text = it.Message.Body
	}
	head := run + " " + it.Kind
	if id != "" {
		head += " " + id
	}
	lines := []string{head + ": " + printable(text)}
	add := func(format string, a ...any) { lines = append(lines, "  "+fmt.Sprintf(format, a...)) }
	quoted := shellquote.Quote(id)
	switch it.Kind {
	case protocol.AttentionKindPermission, protocol.AttentionKindQuestion:
		options := answerOptions(it)
		switch {
		case it.Request == nil || len(it.Request.Options) == 0:
			add("answer it in the session's terminal: aether attach %s", run)
		case len(options) == 0:
			add("it offers no allow once or reject once option, and aether answer never answers for always; decide it in the Aether dashboard")
		default:
			if it.Request.URL != "" {
				add("open %s, then:", it.Request.URL)
			}
			for _, o := range options {
				add("aether answer %s %s %s    # %s", run, quoted, shellquote.Quote(o.ID), printable(o.Label))
			}
			if len(it.Request.Schema) > 0 {
				add("an option that accepts takes --values '<json object>' for the form %s", printable(string(it.Request.Schema)))
			}
		}
	case protocol.AttentionKindRoomQuestion:
		if it.Message == nil {
			add("open the session in the Aether dashboard")
			break
		}
		add("aether answer %s %s \"<answer>\"", run, quoted)
	case protocol.AttentionKindNeedsInput, protocol.AttentionKindBlocked:
		add("aether message %s \"<reply>\"", run)
	case protocol.AttentionKindReady:
		add("aether publish %s", run)
		add("aether close %s --outcome merged", run)
	case protocol.AttentionKindFailed:
		add("aether logs %s", run)
		add("aether message %s \"<reply>\"", run)
		add("aether close %s --outcome abandoned", run)
	default:
		add("open the session in the Aether dashboard")
	}
	return lines
}

func printNeeds(w io.Writer, runID string, items []protocol.AttentionItem) error {
	for _, it := range items {
		if it.RunID != runID {
			continue
		}
		if _, err := fmt.Fprintln(w, strings.Join(needLines(it), "\n")); err != nil {
			return err
		}
	}
	return nil
}

func controlHeld(runID string, err error) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeConflict {
		return err
	}
	var data struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(perr.Data, &data) != nil || data.Reason != protocol.ErrorReasonControlHeld {
		return err
	}
	return fmt.Errorf("%s; ask the member in control to answer, or send your answer to the agent with aether message %s -", perr.Message, runID)
}

func answer(c *protocol.Client, w io.Writer, stdin io.Reader, runID, id, reply string, values map[string]any) error {
	r, err := getRun(c, runID)
	if err != nil {
		return err
	}
	// attention.list shows a sudo request only to those who may allow it,
	// while anyone who can steer may deny it; the server says which.
	if strings.HasPrefix(id, domain.SudoRequestPrefix) {
		if values != nil {
			return fmt.Errorf("--values is for an agent's question; %s is a sudo request", id)
		}
		if err = c.Call(protocol.MethodRunInputRespond, protocol.RunInputRespondParams{RunID: r.ID, RequestID: id, OptionID: reply}, nil); err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "session %s sudo request %s: answered %s\n", r.ID, id, reply)
		return err
	}
	items, err := attentionItems(c, r.WorkspaceID)
	if err != nil {
		return err
	}
	var it *protocol.AttentionItem
	for i := range items {
		if items[i].RunID == r.ID && answerID(items[i]) == id {
			it = &items[i]
		}
	}
	if it == nil {
		if slices.ContainsFunc(r.PendingInputs, func(in domain.RunInputRequest) bool { return in.ID == id }) {
			return fmt.Errorf("request %s on session %s is queued behind the one the server is showing; answer that one first, then aether sessions %s shows the next", id, r.ID, r.ID)
		}
		return fmt.Errorf("session %s has nothing pending for you with id %s; it may be answered already. aether sessions %s lists what is pending", r.ID, id, r.ID)
	}
	if values != nil && it.Kind != protocol.AttentionKindQuestion {
		return fmt.Errorf("--values is for an agent's question; %s is a %s", id, it.Kind)
	}
	key := "cli-answer-" + id
	switch it.Kind {
	case protocol.AttentionKindRoomQuestion:
		if it.Message == nil {
			break
		}
		text, readErr := stdinText(reply, "answer", stdin)
		if readErr != nil {
			return readErr
		}
		var res protocol.RunRoomPostResult
		if err = c.Call(protocol.MethodRunRoomPost, protocol.RunRoomPostParams{WorkspaceID: it.WorkspaceID, RunID: r.ID,
			Kind: protocol.RoomMessageReply, Body: text, CorrelationID: id, IdempotencyKey: key}, &res); err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "replied to question %s on session %s\n", id, r.ID)
		return err
	case protocol.AttentionKindQuestion, protocol.AttentionKindPermission:
		if it.Request == nil || len(it.Request.Options) == 0 {
			return fmt.Errorf("session %s's %s is answered in its terminal: aether attach %s", r.ID, it.Kind, r.ID)
		}
		var picked *protocol.AttentionOption
		options := answerOptions(*it)
		for i := range options {
			if options[i].ID == reply {
				picked = &options[i]
			}
		}
		if picked == nil {
			ids := make([]string, 0, len(options))
			for _, o := range options {
				ids = append(ids, o.ID)
			}
			instead := "decide it in the Aether dashboard"
			if len(ids) > 0 {
				instead = "pick one of " + strings.Join(ids, ", ") + ", or " + instead
			}
			for _, o := range it.Request.Options {
				if o.ID == reply {
					return fmt.Errorf("option %s is not allow once or reject once, and aether answer never answers for always: %s", reply, instead)
				}
			}
			return fmt.Errorf("session %s's %s %s has no option %s: %s", r.ID, it.Kind, id, reply, instead)
		}
		var res protocol.RunInputRespondResult
		if err = c.Call(protocol.MethodRunInputRespond, protocol.RunInputRespondParams{RunID: r.ID, RequestID: id,
			OptionID: picked.ID, Values: values}, &res); err != nil {
			return controlHeld(r.ID, err)
		}
		if res.Status == protocol.RunInputRespondAlreadyAnswered {
			_, err = fmt.Fprintf(w, "session %s %s %s: already answered\n", r.ID, it.Kind, id)
			return err
		}
		_, err = fmt.Fprintf(w, "session %s %s %s: answered %s (%s)\n", r.ID, it.Kind, id, picked.ID, printable(picked.Label))
		return err
	}
	return fmt.Errorf("session %s's %s is not answered with aether answer:\n%s", r.ID, it.Kind, strings.Join(needLines(*it), "\n"))
}
