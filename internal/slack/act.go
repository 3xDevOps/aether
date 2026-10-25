package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// eventCallback is an Events API delivery of message.im, the one event
// the bot subscribes to.
type eventCallback struct {
	TeamID  string `json:"team_id"`
	EventID string `json:"event_id"`
	Event   struct {
		Type        string `json:"type"`
		Subtype     string `json:"subtype"`
		BotID       string `json:"bot_id"`
		User        string `json:"user"`
		Channel     string `json:"channel"`
		ChannelType string `json:"channel_type"`
		Text        string `json:"text"`
		TS          string `json:"ts"`
		ThreadTS    string `json:"thread_ts"`
	} `json:"event"`
}

// interaction is a block_actions payload: a button pressed on the message
// at MessageTS.
type interaction struct {
	Type string `json:"type"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Team struct {
		ID string `json:"id"`
	} `json:"team"`
	Container struct {
		ChannelID string `json:"channel_id"`
		MessageTS string `json:"message_ts"`
	} `json:"container"`
	Actions []struct {
		Value string `json:"value"`
	} `json:"actions"`
}

func (s *Service) event(ctx context.Context, cb eventCallback) {
	ev := cb.Event
	// The bot hears its own messages, and edits and other subtypes, too.
	if ev.Type != "message" || ev.ChannelType != "im" || ev.Subtype != "" || ev.BotID != "" || ev.User == "" {
		return
	}
	if ev.ThreadTS != "" && ev.ThreadTS != ev.TS {
		s.reply(ctx, ev.Channel, ev.ThreadTS, s.message(ctx, cb))
		return
	}
	s.reply(ctx, ev.Channel, "", s.link(ctx, cb))
}

func (s *Service) link(ctx context.Context, cb eventCallback) string {
	ev := cb.Event
	if !store.IsApprovalCode(ev.Text) {
		_, err := s.cfg.Store.SlackLinkByUser(ctx, cb.TeamID, ev.User)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return "To link your Aether account, run `aether slack link` and send me the code it prints."
		case err != nil:
			return err.Error()
		}
		return "Reply in a run's thread to message that run. I start nothing myself."
	}
	now := s.now()
	grant, ok := s.redeem(ev.Text, now)
	if !ok {
		return "That code is wrong, expired or already used. Run `aether slack link` for a new one."
	}
	link := &store.SlackLink{Member: grant.Member, Device: grant.Device, TeamID: cb.TeamID, UserID: ev.User, Channel: ev.Channel, CreatedAt: now}
	if _, err := s.local(ctx, link); err != nil {
		return err.Error()
	}
	member, err := s.cfg.Store.GetMember(ctx, grant.Member)
	if err == nil {
		err = s.cfg.Store.PutSlackLink(ctx, link)
	}
	if err != nil {
		return "Linking failed: " + err.Error()
	}
	return fmt.Sprintf("Linked to %s on %s. Runs that need you arrive here; reply in a run's thread to message it. To unlink, run `aether slack unlink`.",
		member.DisplayName, s.cfg.Name)
}

func (s *Service) message(ctx context.Context, cb eventCallback) string {
	ev := cb.Event
	last, err := s.cfg.Store.SlackMessageAt(ctx, ev.Channel, ev.ThreadTS)
	if errors.Is(err, store.ErrNotFound) {
		return "This thread is not a run's. Reply in the thread of a run's message to message that run."
	}
	if err != nil {
		return err.Error()
	}
	c, err := s.acting(ctx, cb.TeamID, ev.User)
	if err != nil {
		return err.Error()
	}
	var res protocol.RunRoomPostResult
	if err := call(ctx, c, protocol.MethodRunInject, protocol.RunInjectParams{RunID: string(last.RunID), Message: strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(ev.Text),
		IdempotencyKey: "slack-" + cb.EventID}, &res); err != nil {
		return err.Error()
	}
	switch m := res.Message; {
	case m.State == protocol.RoomMessageQueued && m.DeliverAfter != nil:
		return fmt.Sprintf("Queued: the member in control can approve or deny it, else it is delivered at %s.", *m.DeliverAfter)
	case m.State == protocol.RoomMessageSent:
		return "Sent to the run."
	default:
		return fmt.Sprintf("The message is %s.", m.State)
	}
}

func (s *Service) interact(ctx context.Context, in interaction) {
	if in.Type != "block_actions" || len(in.Actions) != 1 {
		return
	}
	s.reply(ctx, in.Container.ChannelID, in.Container.MessageTS, s.press(ctx, in))
}

func (s *Service) press(ctx context.Context, in interaction) string {
	var a answer
	if err := json.Unmarshal([]byte(in.Actions[0].Value), &a); err != nil {
		return "This button answers nothing: " + err.Error()
	}
	last, err := s.cfg.Store.SlackMessageAt(ctx, in.Container.ChannelID, in.Container.MessageTS)
	if errors.Is(err, store.ErrNotFound) || (err == nil && string(last.RunID) != a.Run) {
		return "This message no longer follows the run; answer it in the Aether dashboard."
	}
	if err != nil {
		return err.Error()
	}
	c, err := s.acting(ctx, in.Team.ID, in.User.ID)
	if err != nil {
		return err.Error()
	}
	it, err := pending(ctx, c, last.RunID, "", a.Answer)
	if err != nil {
		return err.Error()
	}
	if it == nil {
		return "Already answered, or no longer pending."
	}
	var picked *choice
	for _, ch := range choices(it) {
		if ch.option == a.Option {
			picked = &ch
		}
	}
	if picked == nil {
		return "The request changed; answer it in the Aether dashboard."
	}
	var res protocol.RunInputRespondResult
	if err := call(ctx, c, protocol.MethodRunInputRespond, protocol.RunInputRespondParams{RunID: a.Run, RequestID: a.Answer,
		OptionID: a.Option}, &res); err != nil {
		var perr *protocol.Error
		var data struct {
			Reason string `json:"reason"`
		}
		if errors.As(err, &perr) && perr.Code == protocol.CodeConflict && json.Unmarshal(perr.Data, &data) == nil &&
			data.Reason == protocol.ErrorReasonControlHeld {
			return strings.TrimSuffix(perr.Message, ".") + "; ask the member in control to answer, or reply in this thread to tell the agent"
		}
		return err.Error()
	}
	if res.Status == protocol.RunInputRespondAlreadyAnswered {
		return "Already answered."
	}
	return "Answered: " + picked.label + "."
}

// acting is the client the Slack user acts through, once one of their
// actions is taken.
func (s *Service) acting(ctx context.Context, team, user string) (Caller, error) {
	link, err := s.cfg.Store.SlackLinkByUser(ctx, team, user)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errors.New("your Slack account is not linked to Aether; run `aether slack link` and send me the code")
	}
	if err != nil {
		return nil, err
	}
	c, err := s.local(ctx, link)
	if err != nil {
		return nil, err
	}
	if err := s.allowWrite(link, s.now()); err != nil {
		return nil, err
	}
	return c, nil
}
