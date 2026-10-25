package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

func init() {
	register(command{
		name:  "answer",
		short: "answer a run's question or permission request (aether inbox lists them)",
		run:   runAnswer,
	})
}

const answerUsage = "usage: aether answer <run-id> <request-id> <option-id> [--values '<json object>']\n" +
	"   or: aether answer <run-id> <question-id> \"<answer>\"|-   (a swarm or teammate question)"

func runAnswer(args []string) error {
	if len(args) < 3 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		return errors.New(answerUsage)
	}
	fs := flag.NewFlagSet("answer", flag.ExitOnError)
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
	case it.Approval != nil:
		return it.Approval.ID
	case it.Request != nil:
		return it.Request.ID
	case it.Message != nil:
		return it.Message.ID
	}
	return ""
}

// answerOptions are the options aether answer picks from. A permission
// request offers only allow once and reject once: an always option would
// change the agent's policy for later requests nobody confirmed.
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
	case it.Approval != nil:
		text = it.Approval.Action
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
	case protocol.AttentionKindApproval:
		add("aether inbox approve %s", quoted)
		add("aether inbox deny %s", quoted)
	case protocol.AttentionKindPermission, protocol.AttentionKindQuestion:
		options := answerOptions(it)
		switch {
		case it.Request == nil || len(it.Request.Options) == 0:
			add("answer it in the run's terminal: aether attach %s", run)
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
	case protocol.AttentionKindSwarmQuestion, protocol.AttentionKindRoomQuestion:
		if it.Message == nil {
			add("open the run in the Aether dashboard")
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
	case protocol.AttentionKindIntegratorDown:
		add("aether swarm replace-integrator %s --agent <agent>", it.MissionID)
	default:
		add("open the run in the Aether dashboard")
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
		return fmt.Errorf("run %s has nothing pending for you with id %s; it may be answered already. aether inbox lists what is pending", r.ID, id)
	}
	if values != nil && it.Kind != protocol.AttentionKindQuestion {
		return fmt.Errorf("--values is for an agent's question; %s is a %s", id, it.Kind)
	}
	key := "cli-answer-" + id
	switch it.Kind {
	case protocol.AttentionKindSwarmQuestion, protocol.AttentionKindRoomQuestion:
		if it.Message == nil {
			break
		}
		text, readErr := stdinText(reply, "answer", stdin)
		if readErr != nil {
			return readErr
		}
		if it.Kind == protocol.AttentionKindSwarmQuestion {
			return answerSwarmQuestion(c, w, it.MissionID, id, text, key)
		}
		var res protocol.RunRoomPostResult
		if err = c.Call(protocol.MethodRunRoomPost, protocol.RunRoomPostParams{WorkspaceID: it.WorkspaceID, RunID: r.ID,
			Kind: protocol.RoomMessageReply, Body: text, CorrelationID: id, IdempotencyKey: key}, &res); err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "replied to question %s on run %s\n", id, r.ID)
		return err
	case protocol.AttentionKindQuestion, protocol.AttentionKindPermission:
		if it.Request == nil || len(it.Request.Options) == 0 {
			return fmt.Errorf("run %s's %s is answered in its terminal: aether attach %s", r.ID, it.Kind, r.ID)
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
			return fmt.Errorf("run %s's %s %s has no option %s: %s", r.ID, it.Kind, id, reply, instead)
		}
		var res protocol.RunInputRespondResult
		if err = c.Call(protocol.MethodRunInputRespond, protocol.RunInputRespondParams{RunID: r.ID, RequestID: id,
			OptionID: picked.ID, Values: values}, &res); err != nil {
			return controlHeld(r.ID, err)
		}
		if res.Status == protocol.RunInputRespondAlreadyAnswered {
			_, err = fmt.Fprintf(w, "run %s %s %s: already answered\n", r.ID, it.Kind, id)
			return err
		}
		_, err = fmt.Fprintf(w, "run %s %s %s: answered %s (%s)\n", r.ID, it.Kind, id, picked.ID, printable(picked.Label))
		return err
	}
	return fmt.Errorf("run %s's %s is not answered with aether answer:\n%s", r.ID, it.Kind, strings.Join(needLines(*it), "\n"))
}
