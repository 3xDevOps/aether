package push

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// burstWindow is how long after a push further ones join it.
const burstWindow = 2 * time.Minute

// burst is a push and those that joined it within burstWindow, one line
// per item, the newest push of each. An item is a session, so its own
// need and a watch's notice about it count once, or else a notification
// tag. tag and topic are the first push's, whose notification every
// summary replaces in place.
type burst struct {
	start      time.Time
	tag, topic string
	keys       []string
	lines      map[string]string
	needs      map[string]bool
	// summarized is set once a summary replaced the first push.
	summarized bool
}

// summary is a burst's notification as it is sent.
type summary struct {
	payload []byte
	topic   string
	urgency string
}

// join adds a push about key to member's newest burst and returns the
// summary to send in place of the burst's notification. It returns nil
// when the push goes out as it is: it starts a burst, or the burst holds
// no other item. A summary alerts only when it brings the burst its first
// need; otherwise it replaces the notification quietly.
func (s *Service) join(member domain.MemberID, key, tag, topic, title, body string, need bool) (*summary, error) {
	line := clip(plain(title), digestTitleRunes)
	if body = plain(body); body != "" {
		line += ": " + clip(body, digestBodyRunes)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	bursts := s.bursts[member]
	var b *burst
	if n := len(bursts); n > 0 && now.Before(bursts[n-1].start.Add(burstWindow)) {
		b = bursts[n-1]
	} else {
		b = &burst{start: now, tag: tag, topic: topic, lines: map[string]string{}, needs: map[string]bool{}}
		s.bursts[member] = append(bursts, b)
	}
	first := need && !slices.ContainsFunc(b.keys, func(k string) bool { return b.needs[k] })
	if !slices.Contains(b.keys, key) {
		b.keys = append(b.keys, key)
	}
	b.lines[key], b.needs[key] = line, need
	if len(b.keys) == 1 {
		return nil, nil
	}
	b.summarized = true
	return b.summary(!first, false)
}

// unjoin drops key's line from every burst whose summary shows it: its
// need ended. A summary still holding a need is sent again without it,
// quietly; one holding none is cleared, as a need that ended is.
func (s *Service) unjoin(ctx context.Context, key string) {
	type update struct {
		member domain.MemberID
		s      *summary
	}
	var updates []update
	s.mu.Lock()
	for member, bursts := range s.bursts {
		for _, b := range bursts {
			if !slices.Contains(b.keys, key) {
				continue
			}
			b.keys = slices.DeleteFunc(b.keys, func(k string) bool { return k == key })
			delete(b.lines, key)
			delete(b.needs, key)
			if !b.summarized {
				continue
			}
			out, err := b.summary(true, true)
			if err != nil {
				slog.Warn("push: encode a summary", "member", member, "error", err)
				continue
			}
			updates = append(updates, update{member, out})
		}
		s.bursts[member] = slices.DeleteFunc(bursts, func(b *burst) bool {
			return !slices.ContainsFunc(b.keys, func(k string) bool { return b.needs[k] })
		})
		if len(s.bursts[member]) == 0 {
			delete(s.bursts, member)
		}
	}
	s.mu.Unlock()
	for _, u := range updates {
		if _, err := s.deliverPayload(ctx, u.member, u.s.payload, u.s.topic, u.s.urgency); err != nil {
			slog.Warn("push: update a summary", "member", u.member, "error", err)
		}
	}
}

// summary is the burst's notification: one line per item, under the first
// push's tag, at high urgency while it holds a need. Once an item ended,
// one with no need left is the quiet replacement a need that ended gets.
func (b *burst) summary(quiet, ended bool) (*summary, error) {
	needs := 0
	lines := make([]string, 0, len(b.keys))
	for _, key := range b.keys {
		if b.needs[key] {
			needs++
		}
		lines = append(lines, b.lines[key])
	}
	n := notification{Body: clearedBody, Clear: true}
	if needs > 0 || !ended {
		title := fmt.Sprintf("%d need you", len(lines))
		if needs < len(lines) {
			title = fmt.Sprintf("%d notifications", len(lines))
		}
		shown := lines[:min(len(lines), digestLines)]
		body := strings.Join(shown, "\n")
		if more := len(lines) - len(shown); more > 0 {
			body += fmt.Sprintf("\n+%d more", more)
		}
		n = notification{Title: title, Body: body, Quiet: quiet}
	}
	payload, err := json.Marshal(message{notification: n, URL: "/", Tag: b.tag})
	if err != nil {
		return nil, fmt.Errorf("push: encode a summary: %w", err)
	}
	urgency := urgencyNormal
	if needs > 0 {
		urgency = urgencyHigh
	}
	return &summary{payload: payload, topic: b.topic, urgency: urgency}, nil
}
