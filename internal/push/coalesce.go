package push

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// burstWindow is how long after a push further ones join it.
const burstWindow = 2 * time.Minute

// burst is a push and those that joined it within burstWindow, one line
// per notification tag, the newest push of each. tag and topic are the
// first push's, which every summary replaces in place.
type burst struct {
	start      time.Time
	tag, topic string
	tags       []string
	lines      map[string]string
	needs      map[string]bool
}

// join adds a push under tag to member's burst and returns the summary to
// send in place of the burst's notification, under the returned topic. It
// returns nil when the push goes out as it is: it starts a burst, the one
// inside the window has no other notification, or it is the burst's own.
func (s *Service) join(member domain.MemberID, tag, topic, title, body string, need bool) ([]byte, string, error) {
	line := clip(plain(title), digestTitleRunes)
	if body = plain(body); body != "" {
		line += ": " + clip(body, digestBodyRunes)
	}
	now := s.now()
	s.mu.Lock()
	b := s.bursts[member]
	if b == nil || !now.Before(b.start.Add(burstWindow)) {
		b = &burst{start: now, tag: tag, topic: topic, lines: map[string]string{}, needs: map[string]bool{}}
		s.bursts[member] = b
	}
	if !slices.Contains(b.tags, tag) {
		b.tags = append(b.tags, tag)
	}
	b.lines[tag], b.needs[tag] = line, need
	if len(b.tags) == 1 {
		s.mu.Unlock()
		return nil, "", nil
	}
	summary := b.summary()
	s.mu.Unlock()
	payload, err := json.Marshal(summary)
	if err != nil {
		return nil, "", fmt.Errorf("push: encode a summary: %w", err)
	}
	return payload, b.topic, nil
}

// summary replaces the burst's notification quietly. It is a notice, so a
// clear of the first push's need leaves it shown.
func (b *burst) summary() message {
	title := fmt.Sprintf("%d need you", len(b.tags))
	lines := make([]string, 0, len(b.tags))
	for _, tag := range b.tags {
		if !b.needs[tag] {
			title = fmt.Sprintf("%d notifications", len(b.tags))
		}
		lines = append(lines, b.lines[tag])
	}
	shown := lines[:min(len(lines), digestLines)]
	body := strings.Join(shown, "\n")
	if more := len(lines) - len(shown); more > 0 {
		body += fmt.Sprintf("\n+%d more", more)
	}
	return message{notification: notification{Title: title, Body: body, Notice: true, Quiet: true}, URL: "/", Tag: b.tag}
}
