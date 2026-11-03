package push

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	DeviceDelivered = "delivered"
	DeviceFailed    = "failed"
)

// DeviceResult is what one of a member's devices' push service said. Err
// is set when it did not take the message; a device the push service
// reports gone is unsubscribed, as every send does.
type DeviceResult struct {
	Status string
	Err    error
}

// Message is a notification for Deliver. URL is the dashboard path a click
// opens. Notice keeps it shown when the run's need ends and is cleared.
// Topic also tags it on the device, apart from the run's own notification,
// so the run's clears never replace it; Clear quietly replaces the one
// under the same tag.
type Message struct {
	Title, Body, URL string
	Run              domain.RunID
	Topic            string
	Urgent           bool
	Notice           bool
	Clear            bool
	Card             string
}

type message struct {
	notification
	URL string `json:"url,omitempty"`
	Tag string `json:"tag,omitempty"`
}

// Deliver does not wait for member to leave a dashboard. No result means
// member has no device.
func (s *Service) Deliver(ctx context.Context, member domain.MemberID, m Message) ([]DeviceResult, error) {
	n := notification{Title: clip(m.Title, fallbackTitleRunes), Body: clip(m.Body, maxBodyRunes), Run: string(m.Run), Notice: m.Notice, Card: m.Card}
	if m.Clear {
		n.Body, n.Clear = cmp.Or(n.Body, clearedBody), true
	}
	payload, err := json.Marshal(message{notification: n, URL: m.URL, Tag: m.Topic})
	if err != nil {
		return nil, fmt.Errorf("push: encode notification: %w", err)
	}
	urgency := urgencyNormal
	if m.Urgent {
		urgency = urgencyHigh
	}
	topic := ""
	if m.Topic != "" {
		topic = topicOf(m.Topic)
	}
	return s.deliverPayload(ctx, member, payload, topic, urgency)
}

// Push is Deliver for a new notification: within burstWindow of member's
// last push it replaces that one with a summary of both, and reports
// joined. m.Topic must be set.
func (s *Service) Push(ctx context.Context, member domain.MemberID, m Message, need bool) ([]DeviceResult, bool, error) {
	// A session's need and a watch's need about it are one item; its other
	// news is not, so checks finishing never hide its question.
	key := m.Topic
	if need && m.Run != "" {
		key = string(m.Run)
	}
	sum, err := s.join(member, key, m.Topic, topicOf(m.Topic), m.Title, m.Body, need)
	if err != nil {
		return nil, false, err
	}
	if sum == nil {
		results, deliverErr := s.Deliver(ctx, member, m)
		return results, false, deliverErr
	}
	results, err := s.deliverPayload(ctx, member, sum.payload, sum.topic, sum.urgency)
	return results, true, err
}

func (s *Service) deliverPayload(ctx context.Context, member domain.MemberID, payload []byte, topic, urgency string) ([]DeviceResult, error) {
	subs, err := s.store.ListPushSubscriptions(ctx, member)
	if err != nil {
		return nil, fmt.Errorf("push: list subscriptions: %w", err)
	}
	results := make([]DeviceResult, 0, len(subs))
	for _, sub := range subs {
		r := DeviceResult{Status: DeviceDelivered}
		if err := s.send(ctx, sub, payload, topic, urgency); err != nil {
			r.Status, r.Err = DeviceFailed, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (s *Service) Devices(ctx context.Context, member domain.MemberID) (int, error) {
	subs, err := s.store.ListPushSubscriptions(ctx, member)
	if err != nil {
		return 0, fmt.Errorf("push: list subscriptions: %w", err)
	}
	return len(subs), nil
}

// HeldUntil is when member will have been away from every dashboard long
// enough for their devices to be told; before then the dashboard shows
// them what a push would.
func (s *Service) HeldUntil(member domain.MemberID) time.Time {
	return s.freeAt(member)
}
