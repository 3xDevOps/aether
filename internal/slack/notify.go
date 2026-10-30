package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// maxRequest is how much of a request a connector shows; a longer one
	// is answered in the dashboard.
	maxRequest = 600
	// maxLabel and maxButtons are Slack's limits for a button's text and
	// an actions block's buttons.
	maxLabel   = 75
	maxButtons = 25
	// maxValue is Slack's limit for a button's value.
	maxValue = 2000
)

type choice struct{ option, label, value string }

// answer is a button's value.
type answer struct {
	Run    string `json:"run"`
	Answer string `json:"answer"`
	Option string `json:"option"`
}

// Need returns an error only when trying again could deliver n: a link
// that no longer works is not retried.
func (s *Service) Need(ctx context.Context, run *domain.Run, n push.Need) error {
	link, err := s.cfg.Store.GetSlackLink(ctx, n.Member)
	if errors.Is(err, store.ErrNotFound) {
		err = nil
	}
	if err != nil {
		return err
	}
	last, err := s.cfg.Store.GetSlackMessage(ctx, run.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return err
	case link == nil || last.Channel != link.Channel:
		if err = s.retire(ctx, last, "This message no longer follows the run."); err != nil {
			return err
		}
		last = nil
	case last.NeedKey == n.Key:
		return nil
	}
	if link == nil {
		return nil
	}
	c, err := s.local(ctx, link)
	if err != nil {
		logged("show a run's need", err, "run", run.ID)
		return nil
	}
	var info protocol.ServerInfoResult
	if err = call(ctx, c, protocol.MethodServerInfo, nil, &info); err != nil {
		return err
	}
	var it *protocol.AttentionItem
	if n.Answer != "" {
		if it, err = pending(ctx, c, run.ID, string(run.WorkspaceID), n.Answer); err != nil {
			return err
		}
	}
	m := render(run, &n, it, info.DashboardURL)
	m.Channel = link.Channel
	if last != nil {
		m.TS = last.TS
		err = s.update(ctx, m)
		if err == nil {
			// Slack does not notify anyone of an edit.
			s.reply(ctx, last.Channel, last.TS, n.Body)
			last.NeedKey = n.Key
			return s.cfg.Store.PutSlackMessage(ctx, last)
		}
		if !lost(err) {
			return err
		}
		m.TS = ""
	}
	ts, err := s.post(ctx, m)
	if err != nil {
		return err
	}
	return s.cfg.Store.PutSlackMessage(ctx, &store.SlackMessage{RunID: run.ID, Channel: link.Channel, TS: ts, NeedKey: n.Key})
}

// Clear shows run data only in its owner's linked DM, so an account
// unlinked since hears nothing more.
func (s *Service) Clear(ctx context.Context, id domain.RunID) error {
	last, err := s.cfg.Store.GetSlackMessage(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || last.NeedKey == "" {
		return err
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return err
	}
	link, err := s.cfg.Store.GetSlackLink(ctx, run.MemberID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if link == nil || link.Channel != last.Channel {
		return s.retire(ctx, last, "This message no longer follows the run.")
	}
	m := render(run, nil, nil, "")
	m.Channel, m.TS = last.Channel, last.TS
	if err = s.update(ctx, m); err != nil && !lost(err) {
		return err
	}
	last.NeedKey = ""
	return s.cfg.Store.PutSlackMessage(ctx, last)
}

// Notice posts a new message, which Slack notifies of, to the member's
// linked DM; a member with no link hears nothing. Like Need, it returns an
// error only when trying again could deliver n.
func (s *Service) Notice(ctx context.Context, member domain.MemberID, n push.Notice) error {
	link, err := s.cfg.Store.GetSlackLink(ctx, member)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	c, err := s.local(ctx, link)
	if err != nil {
		logged("show a workspace notice", err, "member", member)
		return nil
	}
	var info protocol.ServerInfoResult
	if err = call(ctx, c, protocol.MethodServerInfo, nil, &info); err != nil {
		return err
	}
	blocks := []block{section("mrkdwn", "*"+escape(n.Title)+"*\n"+escape(n.Body))}
	if info.DashboardURL != "" {
		open := "<" + info.DashboardURL + "/?page=workspace&id=" + url.QueryEscape(string(n.Workspace)) + "|Open in Aether>"
		blocks = append(blocks, block{"type": "context", "elements": []block{{"type": "mrkdwn", "text": open}}})
	}
	_, err = s.post(ctx, message{Channel: link.Channel, Text: escape(n.Title + ": " + n.Body), Blocks: blocks})
	return err
}

// retire leaves only text on a message, with nothing from its run, and
// forgets the message once Slack has it, or will never let the bot edit
// it, so a message from a replaced app cannot hold up the run's next one.
func (s *Service) retire(ctx context.Context, last *store.SlackMessage, text string) error {
	err := s.update(ctx, message{Channel: last.Channel, TS: last.TS, Text: text, Blocks: []block{section("plain_text", text)}})
	if err != nil && !lost(err) {
		return err
	}
	logged("retire a message", err, "run", last.RunID, "channel", last.Channel)
	return s.cfg.Store.DeleteSlackMessage(ctx, last.RunID)
}

// render escapes, or sends as plain text, everything an agent wrote.
func render(run *domain.Run, n *push.Need, it *protocol.AttentionItem, dashboard string) message {
	title, line := push.RunTitle(run), "Nothing needs you now."
	if n != nil {
		line = n.Body
	}
	blocks := []block{section("mrkdwn", "*"+escape(title)+"*\n"+escape(line))}
	var text string
	if it != nil && it.Request != nil {
		text = it.Request.Title
	}
	if text != "" {
		blocks = append(blocks, section("plain_text", clip(text, maxRequest)))
	}
	buttons := choices(it)
	if len(buttons) > 0 {
		elements := make([]block, 0, len(buttons))
		for i, b := range buttons {
			elements = append(elements, block{"type": "button", "action_id": "answer-" + strconv.Itoa(i),
				"text": block{"type": "plain_text", "text": b.label}, "value": b.value})
		}
		blocks = append(blocks, block{"type": "actions", "elements": elements})
	}
	var notes []string
	if dashboard != "" {
		page := "/?run=" + url.QueryEscape(string(run.ID))
		notes = append(notes, "<"+dashboard+page+"|Open in Aether>")
	}
	switch {
	case it != nil && len(buttons) == 0 && len([]rune(text)) > maxRequest:
		notes = append(notes, "This request is longer than Slack shows; answer it in the Aether dashboard.")
	case it != nil && len(buttons) == 0:
		notes = append(notes, "Answer it in the Aether dashboard.")
	}
	notes = append(notes, "Reply in this thread to message the run.")
	blocks = append(blocks, block{"type": "context", "elements": []block{{"type": "mrkdwn", "text": strings.Join(notes, " · ")}}})
	return message{Text: escape(title + ": " + line), Blocks: blocks}
}

func section(kind, text string) block {
	return block{"type": "section", "text": block{"type": kind, "text": text}}
}

// choices are the buttons it is answered with in Slack. There are none
// when Slack would show its text or a label cut, since a press would
// decide text nobody saw, or when it takes more than a choice.
func choices(it *protocol.AttentionItem) []choice {
	if it == nil || it.Request == nil || len(it.Request.Schema) > 0 || it.Request.URL != "" ||
		(it.Kind != protocol.AttentionKindPermission && it.Kind != protocol.AttentionKindQuestion) ||
		len([]rune(it.Request.Title)) > maxRequest {
		return nil
	}
	var out []choice
	for _, o := range it.Request.Options {
		// An always option would change what the agent may do next time,
		// which nobody confirmed.
		if it.Kind == protocol.AttentionKindPermission && o.Kind != "allow_once" && o.Kind != "reject_once" {
			continue
		}
		if len([]rune(o.Label)) > maxLabel {
			return nil
		}
		out = append(out, choice{option: o.ID, label: o.Label})
	}
	if len(out) > maxButtons {
		return nil
	}
	for i := range out {
		value, err := json.Marshal(answer{Run: it.RunID, Answer: it.Request.ID, Option: out[i].option})
		if err != nil || len(value) > maxValue {
			return nil
		}
		out[i].value = string(value)
	}
	return out
}

func pending(ctx context.Context, c Caller, run domain.RunID, workspace, id string) (*protocol.AttentionItem, error) {
	var res protocol.AttentionListResult
	if err := call(ctx, c, protocol.MethodAttentionList, protocol.AttentionListParams{WorkspaceID: workspace}, &res); err != nil {
		return nil, err
	}
	for _, it := range res.Items {
		if it.RunID == string(run) && it.Request != nil && it.Request.ID == id {
			return &it, nil
		}
	}
	return nil, nil
}

func call(ctx context.Context, c Caller, method string, params, out any) error {
	var raw json.RawMessage
	if params != nil {
		var err error
		if raw, err = json.Marshal(params); err != nil {
			return err
		}
	}
	res, perr := c.Call(ctx, method, raw)
	if perr != nil {
		return perr
	}
	return json.Unmarshal(res, out)
}

func clip(text string, n int) string {
	if r := []rune(text); len(r) > n {
		return string(r[:n]) + "…"
	}
	return text
}
